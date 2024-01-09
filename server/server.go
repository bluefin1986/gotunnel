package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

type ClientInfo struct {
	conn      net.Conn
	localPort int
}

var (
	mu                sync.Mutex
	connectionsMap    = make(map[string]net.Conn)
	idCounter         int
	heartbeatBytes    = []byte("&hb\n")
	startForwardBytes = []byte("&st\n")
	cmdReceived       = []byte("0")
	logDebug          = false
)

func debugLog(format string, args ...interface{}) {
	if logDebug {
		fmt.Printf(format, args...)
	}
}

func handleClient(clientConn net.Conn, wg *sync.WaitGroup, clientInfo *ClientInfo) {
	defer wg.Done()
	// 关闭超时
	clientConn.SetReadDeadline(time.Time{})

	// 启动心跳 goroutine
	go sendHeartbeat(clientConn, clientInfo)

	mu.Lock()
	*clientInfo = ClientInfo{
		conn: clientConn,
	}
	mu.Unlock()

	fmt.Println("Client connected.")

	// 等待 client.go 发送完信息后再进行流量转发
	wg.Wait()
}

func handleOtherClient(otherClientConn net.Conn, clientInfo *ClientInfo) {
	id := generateID()

	mu.Lock()
	connectionsMap[id] = otherClientConn
	mu.Unlock()

	sendStartForward(clientInfo.conn)

	// 开始进行流量转发
	go func() {
		copyData(clientInfo.conn, otherClientConn, id, "other->client")
	}()

	go func() {
		copyData(otherClientConn, clientInfo.conn, id, "client->other")
	}()
}

func generateID() string {
	idCounter++
	return fmt.Sprintf("conn%d", idCounter)
}

func copyData(dst net.Conn, src net.Conn, id string, direction string) {
	buf := make([]byte, 1024)
	for {
		// mu.Lock()
		_, exists := connectionsMap[id]
		// mu.Unlock()

		if !exists {
			fmt.Printf("Connection with id %s does not exist. Closing copyData %s\n", id, direction)
			break
		}
		// 设置读取截止时间为10毫秒，免得断开检测过长
		err := src.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
		if err != nil {
			fmt.Println("Error setting read deadline:", err)
			break
		}

		// 读取数据
		n, err := src.Read(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// 超时，说明连接还在，继续下一轮循环
				continue
			}

			if err == io.EOF {
				// 连接关闭
				fmt.Println("Connection closed by the other side.")
				// continue
			} else {
				fmt.Println("Error reading data:", err)
			}
			break
		}

		// 没有bytes需要被转发，跳过
		if n == 0 {
			fmt.Printf("no bytes need to be write [%s], continue.\n", direction)
			continue
		}

		// 打印调试信息
		debugLog("Copying %d bytes from %s to %s\n", n, src.RemoteAddr(), dst.RemoteAddr())

		// 复制数据
		_, err = dst.Write(buf[:n])
		if err != nil {
			fmt.Printf("Error writing data:%+v\n", err)
			// fmt.Println("Error writing data:", err)
			break
		}
	}
	// 关闭连接
	// mu.Lock()
	if conn, exists := connectionsMap[id]; exists {
		conn.Close()
		delete(connectionsMap, id)
	}
	// mu.Unlock()
	fmt.Printf("Closing copyData %s\n", direction)
}

func sendHeartbeat(conn net.Conn, clientInfo *ClientInfo) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// 定期向客户端发送心跳
			_, err := conn.Write(heartbeatBytes)
			if err != nil {
				mu.Lock()
				clientInfo = nil
				mu.Unlock()
				fmt.Println("Error sending heartbeat:", err)
				fmt.Println("Clear clientInfo")
				return
			}
		}
	}
}

func sendStartForward(conn net.Conn) bool {
	_, err := conn.Write(startForwardBytes)
	if err != nil {
		fmt.Println("send start forward command failed", err)
		return false
	}
	buf := make([]byte, 10)
	n, err := conn.Read(buf)
	if err != nil {
		fmt.Println("read received status failed", err)
		return false
	}
	resp := bytes.TrimSpace(buf[:n])
	if !bytes.Equal(resp, cmdReceived) {
		fmt.Println("read resp [%s], not match anything", string(resp))
		return false
	}
	return true
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

	var wg sync.WaitGroup
	var clientInfo ClientInfo

	for {
		var clientConn net.Conn
		fmt.Printf("Server listening on port %d...\n", port)
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

		wg.Add(1)

		mu.Lock()
		// 客户端还没连接上来，尝试接收连接的指令，判断是不是自己的客户端
		if clientInfo.conn == nil {
			mu.Unlock()
			// 创建一个新的 Reader，确保 handleClient 中的读取不影响 main 中的读取
			reader := io.MultiReader(clientConn, strings.NewReader("\n"))

			// 读取客户端指令
			scanner := bufio.NewScanner(reader)
			scanner.Scan()
			command := scanner.Text()
			fmt.Printf("Received command: %s\n", command)
			if strings.TrimSpace(command) == "sp" {
				fmt.Printf("Received client conn: %s\n", clientConn.RemoteAddr())
				// 处理 client.go 的连接
				go handleClient(clientConn, &wg, &clientInfo)
			}
		} else {
			mu.Unlock()
			fmt.Printf("Received other client conn: %s\n", clientConn.RemoteAddr())
			// 处理其他类型的连接
			go handleOtherClient(clientConn, &clientInfo)
		}
	}
}
