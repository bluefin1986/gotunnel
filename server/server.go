package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	CMD_HEART_BEAT           = "&hb"
	CMD_CLIENT_HELLO         = "&sp"
	CMD_CONNECT_CHANNEL      = "&cc"
	CMD_ACK_SUCC             = "&00"
	CMD_ACK_FAIL             = "&01"
	CMD_CONNECT_CHANNEL_RESP = "ccr"
	CMD_DELIMITER            = ":"
)

// ConnectionInfo 包含连接信息
type ConnectionInfo struct {
	ID              string
	tunnelConn      net.Conn // 通道连接，也就是自己的客户端连接
	clientConn      net.Conn // 客户端连接，一般指的是其他客户端软件的连接
	firstBatchBytes []byte   // 首包消息，其他客户端连接上来以后，暂存首包消息，等自己的客户端连接上来以后转发
}

var (
	connectionsMap = make(map[string]ConnectionInfo)
	cmdConn        net.Conn
	mu             sync.Mutex
	muCmd          sync.Mutex
	idCounter      int
	logDebug       = false
)

func debugLog(format string, args ...interface{}) {
	if logDebug {
		fmt.Printf(format, args...)
	}
}

func handleClient(clientConn net.Conn) {
	// 识别客户端类型
	buf := make([]byte, 1024)
	// 设置clientConn的读超时为10毫秒
	err := clientConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if err != nil {
		fmt.Println("Error setting read deadline:", err)
		return
	}

	for {
		// 读取数据
		reader := bufio.NewReader(clientConn)
		n, err := reader.Read(buf)
		if err != nil {
			if err != io.EOF {
				fmt.Println("Read error:", err)
			} else {
				// 连接关闭
				fmt.Println("Connection closed by the clientConn.")
			}
		}
		command := strings.TrimSpace(string(buf[:n]))
		// 打印收到的command
		debugLog("Received command:[%s] from %s\n", command, clientConn.RemoteAddr())
		if strings.HasPrefix(command, CMD_ACK_SUCC) {
			// 客户端指令接受成功，返回
			cmdType := command[len(CMD_ACK_SUCC)+1:]
			switch cmdType {
			case "buildChannel":
				fmt.Printf("build channel cmd is received by client\n")
			default:
				fmt.Printf("unknown cmd ack\n")
			}
			break
		} else if strings.HasPrefix(command, CMD_CONNECT_CHANNEL_RESP) {
			// 客户端连接上来，建立数据通道
			// 取出连接配对ID
			connectionPairId := command[len(CMD_CONNECT_CHANNEL_RESP)+1:]
			// 从connectionsMap 取出对应的connectionInfo
			mu.Lock()
			connectionInfo, exists := connectionsMap[connectionPairId]
			mu.Unlock()
			if !exists {
				errmsg := fmt.Sprintf("connectionPairId[%s] does not exist\n", connectionPairId)
				fmt.Printf(errmsg)
				// 向客户端发送失败指令，告知其连接失败
				clientConn.Write([]byte(CMD_ACK_FAIL + errmsg))
				break
			}
			connectionInfo.tunnelConn = clientConn
			mu.Lock()
			connectionsMap[connectionPairId] = connectionInfo
			mu.Unlock()
			// 把connectionInfo中暂存的来自其他客户端的firstBatchBytes发送给自己的客户端tunnelConn
			_, err := connectionInfo.tunnelConn.Write(connectionInfo.firstBatchBytes)
			if err != nil {
				fmt.Printf("Error writing firstBatchBytes to tunnelConn:%+v\n", err)
				break
			}
			// 建立数据通道
			establishDataChannel(connectionInfo)
			break
		} else if strings.HasPrefix(command, CMD_CLIENT_HELLO) {
			// 客户端连接成功，保存指令通道
			cmdConn = clientConn
			muCmd.Lock()
			cmdConn.Write([]byte(CMD_ACK_SUCC))
			muCmd.Unlock()
			fmt.Printf("Client connected, use it as cmd connection [%s] \n", cmdConn.RemoteAddr())
			go sendHeartbeat()
			break
		} else {
			// 这里就是其他的连接，存起来，等客户端新建连接上来以后配对
			connectionPairId := generateID()
			connectionInfo := ConnectionInfo{
				ID:              connectionPairId,
				clientConn:      clientConn,
				firstBatchBytes: buf[:n],
			}
			mu.Lock()
			connectionsMap[connectionPairId] = connectionInfo
			mu.Unlock()
			fmt.Printf("conn[%s] saved to connectionsMap[%s] , call client to build a new channel\n", clientConn.RemoteAddr(), connectionPairId)
			// 通知客户端建立消息通道
			muCmd.Lock()
			cmdConn.Write([]byte(CMD_CONNECT_CHANNEL + CMD_DELIMITER + connectionPairId + "\n"))
			muCmd.Unlock()
			break
		}
	}
}

func establishDataChannel(connectionInfo ConnectionInfo) {
	go copyData(connectionInfo.clientConn, connectionInfo.tunnelConn, connectionInfo.ID, "tunnel>client")
	go copyData(connectionInfo.tunnelConn, connectionInfo.clientConn, connectionInfo.ID, "client>tunnel")
	debugLog("Data channel established between %s and %s\n", connectionInfo.clientConn.RemoteAddr(), connectionInfo.tunnelConn.RemoteAddr())
}

func generateID() string {
	idCounter++
	return fmt.Sprintf("conn%d", idCounter)
}

func copyData(dst net.Conn, src net.Conn, id string, direction string) {
	defer dst.Close()
	defer src.Close()
	buf := make([]byte, 1024)
	for {
		// 设置读取截止时间为10毫秒，免得断开检测过长
		err := src.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
		if err != nil {
			fmt.Println("Error setting read deadline:", err)
			// break
		}

		// 读取数据
		n, err := src.Read(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// 超时，说明连接还在，继续下一轮循环
				// continue
			}

			if err == io.EOF {
				// 连接关闭
				debugLog("Connection closed by the other side.\n")
				// continue
			} else {
				debugLog("Error reading data:%+v\n", err)
			}
			// break
		}

		// 没有bytes需要被转发，跳过
		if n == 0 {
			// fmt.Printf("no bytes need to be write [%s], break.\n", direction)
			continue
		}

		// 打印调试信息
		debugLog("Copying %d bytes from %s to %s %s, content:[%s]\n", n, src.RemoteAddr(), dst.RemoteAddr(), direction, string(buf[:n]))

		// 复制数据
		_, err = dst.Write(buf[:n])
		if err != nil {
			fmt.Printf("Error writing data:%+v\n", err)
			// fmt.Println("Error writing data:", err)
			break
		}
	}
	// 连接断开，从connectionsMap中删除对应的 connectionInfo
	mu.Lock()
	if _, ok := connectionsMap[id]; !ok {
		fmt.Printf("connectionInfo [%s] already removed\n", id)
		mu.Unlock()
		return
	}
	delete(connectionsMap, id)
	mu.Unlock()
	fmt.Printf("Closing copyData %s\n", direction)
}

func sendHeartbeat() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	if cmdConn == nil {
		fmt.Println("cmdConn is nil, return")
		return
	}

	for {
		select {
		case <-ticker.C:
			// 定期向客户端发送心跳
			muCmd.Lock()
			_, err := cmdConn.Write([]byte(CMD_HEART_BEAT + "\n"))
			muCmd.Unlock()
			if err != nil {
				mu.Lock()
				cmdConn = nil
				mu.Unlock()
				fmt.Println("Error sending heartbeat:", err)
				return
			}
		}
	}
}

func main() {
	port := 6000
	useTLS := false
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	defer listener.Close()
	if err != nil {
		fmt.Println("Error listening:", err)
		return
	}
	fmt.Printf("Server listening on port %d...\n", port)

	for {
		var clientConn net.Conn
		if useTLS {
			// 证书和私钥文件的路径
			certFile := "/path/to/server.crt"
			keyFile := "/path/to/server.key"
			// 使用TLS证书和私钥创建TLS配置
			tlsConfig := &tls.Config{}
			cert, err := tls.LoadX509KeyPair(certFile, keyFile)
			if err != nil {
				fmt.Println("Error loading certificate:", err)
				os.Exit(1)
			}
			tlsConfig.Certificates = []tls.Certificate{cert}

			// 使用tls.NewListener创建TLS监听器
			tlsListener := tls.NewListener(listener, tlsConfig)
			clientConn, err = tlsListener.Accept()
		} else {
			clientConn, err = listener.Accept()
		}
		if err != nil {
			fmt.Println("Error accepting connection:", err)
			continue
		}
		//打印客户端连接地址
		fmt.Printf("===>new Client %s connected.\n", clientConn.RemoteAddr())
		handleClient(clientConn)
	}
}
