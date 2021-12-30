package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/sevlyar/go-daemon"
)

// 常量设定
const BUF_SIZE = 8192

const READ = 0
const WRITE = 1
const DEBUG = 1

const DEFAULT_LOCAL_PORT = 8080
const DEFAULT_REMOTE_PORT = 8081
const SERVER_SOCKET_ERROR = -1
const SERVER_SETSOCKOPT_ERROR = -2
const SERVER_BIND_ERROR = -3
const SERVER_LISTEN_ERROR = -4
const CLIENT_SOCKET_ERROR = -5
const CLIENT_RESOLVE_ERROR = -6
const CLIENT_CONNECT_ERROR = -7
const CREATE_PIPE_ERROR = -8
const BROKEN_PIPE_ERROR = -9
const HEADER_BUFFER_FULL = -10
const BAD_HTTP_PROTOCOL = -11

const MAX_HEADER_SIZE = 8192

var remoteAddress string
var localPort int
var serverSock net.Listener

const (
	FLG_NONE = iota /* 正常数据流不进行编解码 */
	R_C_DEC         /* 读取客户端数据仅进行解码 */
	W_S_ENC         /* 发送到服务端进行编码 */
)

var ioFlag int = FLG_NONE /* 网络io的一些标志位 */
var daemonFlag bool = false

func main() {
	setConfig()
	getInfo()
	startServer()

}

// daemon
func startDaemon() {
	ctx := &daemon.Context{
		PidFileName: "/tmp/go-mproxy.pid",
		PidFilePerm: 0644,
		WorkDir:     "./",
		Umask:       027,
	}
	child, err := ctx.Reborn()
	if err != nil {
		log.Fatalf("run failed: %v", err)
	}
	if child != nil {
		return
	}
	defer ctx.Release()
	serverLoop()
}

// 获取参数

func setConfig() {
	flag.IntVar(&localPort, "l", 8080, "<port number> specifyed local listen port ")
	flag.StringVar(&remoteAddress, "h", "", "<remote server and port> specifyed next hop server name")

	flag.BoolVar(&daemonFlag, "d", false, "run as daemon")
	var DFlag bool
	var EFlag bool
	flag.BoolVar(&DFlag, "D", false, "decode data when receiving data")
	flag.BoolVar(&EFlag, "E", false, "encode data when forwarding data")
	var help bool
	flag.BoolVar(&help, "H", false, "help")
	flag.Parse()
	hVarArr := strings.Split(remoteAddress, ":")
	if remoteAddress != "" && len(hVarArr) != 2 {
		flag.PrintDefaults()
		os.Exit(1)
	}
	if help {
		flag.PrintDefaults()
		os.Exit(1)
	}
	if DFlag {
		ioFlag = R_C_DEC
	}
	if EFlag {
		ioFlag = W_S_ENC
	}
}
func getInfo() {
	fmt.Printf("======= mproxy (v0.1) ========\n")
	fmt.Println(getWorkMode())
}

func getWorkMode() string {
	var info string
	if len(remoteAddress) > 0 {
		info = fmt.Sprintf("start server on %d and next hop is %s\n", localPort, remoteAddress)
		if ioFlag == FLG_NONE {
			info += "start as normal http proxy"
		} else if ioFlag == R_C_DEC {
			info += "start as remote forward proxy and do decode data when recevie data"
		}
	} else {
		info = fmt.Sprintf("start server on %d\n", localPort)
		if ioFlag == FLG_NONE {
			info += "start as remote forward proxy"
		} else if ioFlag == W_S_ENC {
			info += "start as forward proxy and do encode data when send data"
		}
	}

	return info
}

func startServer() {
	//初始化全局变量
	var err error
	serverSock, err = net.Listen("tcp", ":"+strconv.Itoa(localPort))
	if err != nil {
		panic("listen error:" + err.Error())
	}
	goos := runtime.GOOS
	if daemonFlag && goos != "windows" {
		startDaemon()
	} else {
		serverLoop()
	}

}
func serverLoop() {
	for {
		connect, err := serverSock.Accept()
		if err != nil {
			panic("connect error:" + err.Error())
		}
		go handleClient(connect)

	}
}
func handleClient(connect net.Conn) {
	defer connect.Close()
	isHttpTunnel := false
	var header []byte
	var headerRead int
	var remoteHost string
	// 未指定远端主机名称从http 请求 HOST 字段中获取
	if len(remoteAddress) == 0 {
		var err error
		header, headerRead, err = readHeader(connect)
		if err != nil {
			log.Printf("Read Http header failed\n")
			return
		} else {
			log.Printf("header:%s", string(header))
			if string(header[:7]) == "CONNECT" {
				log.Printf("receive CONNECT request\n")
				isHttpTunnel = true
			}
			// 获取目标机地址和端口
			remoteHost = extractHost(isHttpTunnel, header)
			if remoteHost == "" {
				return
			}
		}
	} else {
		remoteHost = remoteAddress
	}
	remoteConn, err := net.Dial("tcp", remoteHost)
	if err != nil {
		log.Printf("remote connect error:%s", err.Error())
		return
	}
	defer remoteConn.Close()
	if err != nil {
		log.Printf("Cannot connect to host%s", remoteHost)
	}
	if !isHttpTunnel && headerRead > 0 {
		header = encByte(header, headerRead)
		remoteConn.Write(header)
	}
	if isHttpTunnel {
		resp := []byte("HTTP/1.1 200 Connection Established\r\n\r\n")
		resp = encByte(resp, len(resp))
		connect.Write(resp)
	}
	connChan := make(chan bool, 1)
	go ioCopy(remoteConn, connect, connChan)
	go ioCopy(connect, remoteConn, connChan)
	<-connChan
}
func extractHost(isHttpTunnel bool, header []byte) string {
	var address string
	headerArr := strings.Split(string(header), "\r\n")
	if len(headerArr) < 2 {
		return ""
	}
	hostArr := strings.SplitAfterN(headerArr[1], ":", 2)
	address = strings.Trim(hostArr[1], " ")
	if !strings.Contains(address, ":") && !isHttpTunnel {
		address += ":80"
	}
	log.Printf("address:%s", address)
	return address
}
func ioCopy(conn1 net.Conn, conn2 net.Conn, connChan chan bool) {
	_, err := copyBuffer(conn1, conn2)
	if err != nil {
		log.Printf("copy error:%s", err.Error())
	}
	connChan <- true

}

func readHeader(connect net.Conn) ([]byte, int, error) {
	header := make([]byte, MAX_HEADER_SIZE)
	n, err := connect.Read(header)
	header = encByte(header, n)

	return header, n, err
}

func copyBuffer(dst io.Writer, src io.Reader) (written int64, err error) {
	var errInvalidWrite = errors.New("invalid write result")
	var ErrShortWrite = errors.New("short write")
	// If the reader has a WriteTo method, use it to do the copy.
	// Avoids an allocation and a copy.
	if ioFlag == FLG_NONE {
		if wt, ok := src.(io.WriterTo); ok {
			return wt.WriteTo(dst)
		}
		// Similarly, if the writer has a ReadFrom method, use it to do the copy.
		if rt, ok := dst.(io.ReaderFrom); ok {
			return rt.ReadFrom(src)
		}
	}

	size := 32 * 1024
	if l, ok := src.(*io.LimitedReader); ok && int64(size) > l.N {
		if l.N < 1 {
			size = 1
		} else {
			size = int(l.N)
		}
	}
	buf := make([]byte, size)
	for {
		nr, er := src.Read(buf)
		buf = encByte(buf, nr)
		if nr > 0 {
			nw, ew := dst.Write(buf[0:nr])
			if nw < 0 || nr < nw {
				nw = 0
				if ew == nil {
					ew = errInvalidWrite
				}
			}
			written += int64(nw)
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = ErrShortWrite
				break
			}
		}
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
	}
	return written, err
}

func encByte(data []byte, n int) []byte {
	if ioFlag != FLG_NONE {
		for i := 0; i < n; i++ {
			data[i] ^= 1
		}
	}
	return data
}
