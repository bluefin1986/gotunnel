package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

var clientInfo *ClientInfo
var mu sync.Mutex
var connectionsMap = make(map[string]net.Conn)
var idCounter int
var heartbeatBytes = []byte("&hb\n")

type ClientInfo struct {
	conn      net.Conn
	localPort int
}

func handleClient(clientConn net.Conn, wg *sync.WaitGroup) {
	defer wg.Done()
	// 关闭超时
	clientConn.SetReadDeadline(time.Time{})

	// 启动心跳 goroutine
	go sendHeartbeat(clientConn)

	mu.Lock()
	clientInfo = &ClientInfo{
		conn: clientConn,
	}
	mu.Unlock()

	fmt.Println("Client connected.")

	// 等待 client.go 发送完信息后再进行流量转发
	wg.Wait()
}

func handleOtherClient(otherClientConn net.Conn) {
	id := generateID()

	mu.Lock()
	connectionsMap[id] = otherClientConn
	mu.Unlock()

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
			fmt.Printf("Connection with id %d does not exist. Closing copyData %s\n", id, direction)
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
		fmt.Printf("Copying %d bytes from %s to %s\n", n, src.RemoteAddr(), dst.RemoteAddr())

		// 复制数据
		_, err = dst.Write(buf[:n])
		if err != nil {
			fmt.Println("Error writing data:", err)
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

func sendHeartbeat(conn net.Conn) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// 定期向客户端发送心跳
			_, err := conn.Write(heartbeatBytes)
			if err != nil {
				clientInfo = nil
				fmt.Println("Error sending heartbeat:", err)
				fmt.Println("Clear clientInfo")
				return
			}
		}
	}
}

func main() {
	port := 6000

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		fmt.Println("Error listening:", err)
		return
	}
	defer listener.Close()

	fmt.Printf("Server listening on port %d...\n", port)

	var wg sync.WaitGroup

	for {
		clientConn, err := listener.Accept()
		if err != nil {
			fmt.Println("Error accepting connection:", err)
			continue
		}

		wg.Add(1)

		mu.Lock()
		// 客户端还没连接上来，尝试接收连接的指令，判断是不是自己的客户端
		if clientInfo == nil {
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
				go handleClient(clientConn, &wg)
			}
		} else {
			mu.Unlock()
			fmt.Printf("Received other client conn: %s\n", clientConn.RemoteAddr())
			// 处理其他类型的连接
			go handleOtherClient(clientConn)
		}
	}
}
