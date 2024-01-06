package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
)

func main() {
	listenPort := 6000
	forwardPort := 8080

	// 启动监听端口
	listen, err := net.Listen("tcp", fmt.Sprintf(":%d", listenPort))
	if err != nil {
		fmt.Println("Error listening:", err)
		return
	}
	defer listen.Close()

	fmt.Printf("Server listening on port %d...\n", listenPort)

	for {
		// 接受客户端连接
		clientConn, err := listen.Accept()
		if err != nil {
			fmt.Println("Error accepting connection:", err)
			continue
		}

		// 创建一个新的 Reader，确保 handleClient 中的读取不影响 main 中的读取
		reader := io.MultiReader(clientConn, strings.NewReader("\n"))

		// 读取客户端指令
		scanner := bufio.NewScanner(reader)
		scanner.Scan()
		command := scanner.Text()
		fmt.Printf("command: %s", command)
		// 启动 goroutine 处理连接
		go forwardTraffic(clientConn, forwardPort)
	}
}

func forwardTraffic(clientConn net.Conn, forwardPort int) {
	defer clientConn.Close()

	// 连接到转发目标端口
	forwardConn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", forwardPort))
	if err != nil {
		fmt.Println("Error connecting to forward port:", err)
		return
	}
	defer forwardConn.Close()

	// 使用 io.Copy 将数据从客户端转发到目标端口
	_, err = io.Copy(forwardConn, clientConn)
	if err != nil {
		fmt.Println("Error copying data:", err)
		return
	}
}
