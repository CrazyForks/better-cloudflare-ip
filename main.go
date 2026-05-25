package main

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 命令行版本的入口
func main() {
	initLocations()
	showMenu()
}

// 显示主菜单
func showMenu() {
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Println("----------------------------------------")
		fmt.Println("1. IPV4 优选 (TLS)")
		fmt.Println("2. IPV4 优选 (非 TLS)")
		fmt.Println("3. IPV6 优选 (TLS)")
		fmt.Println("4. IPV6 优选 (非 TLS)")
		fmt.Println("5. 单 IP 测速 (TLS)")
		fmt.Println("6. 单 IP 测速 (非 TLS)")
		fmt.Println("7. 清空缓存")
		fmt.Println("8. 更新数据")
		fmt.Println("0. 退出")
		fmt.Print("请选择菜单 (默认 0): ")

		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			input = "0"
		}

		switch input {
		case "0":
			fmt.Println("退出成功")
			return
		case "1":
			runIPSelector(4, true)
		case "2":
			runIPSelector(4, false)
		case "3":
			runIPSelector(6, true)
		case "4":
			runIPSelector(6, false)
		case "5":
			runSingleSpeedTest(true)
		case "6":
			runSingleSpeedTest(false)
		case "7":
			clearCache()
		case "8":
			updateData()
		default:
			fmt.Println("无效输入，请重新选择")
		}
	}
}

// runIPSelector 运行 IP 优选流程
func runIPSelector(ipType int, useTLS bool) {
	var bandwidth int
	var taskNum int

	fmt.Print("请设置期望的带宽大小 (默认最小 1，单位 Mbps): ")
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			bandwidth = 1
		} else {
			bandwidth, err := strconv.Atoi(input)
			if err != nil || bandwidth <= 0 {
				fmt.Println("输入无效，已使用默认值 1 Mbps")
				bandwidth = 1
			}
		}
	}

	fmt.Print("请设置 RTT 测试进程数 (默认 50，最大 100): ")
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			taskNum = 50
		} else {
			taskNum, err := strconv.Atoi(input)
			if err != nil {
				fmt.Println("输入无效，已使用默认值 50")
				taskNum = 50
			} else if taskNum <= 0 {
				fmt.Println("进程数不能为 0，自动设置为默认值")
				taskNum = 50
			}
			if taskNum > 100 {
				fmt.Println("超过最大进程限制，自动设置为最大值")
				taskNum = 100
			}
		}
	}

	speed := bandwidth * 128
	startTime := time.Now()

	// 执行 Cloudflare 测试
	anycast, max, avgms, dataCenter := cloudflareTest(ipType, useTLS, taskNum, speed)

	realBandwidth := max / 128
	endTime := time.Now()

	fmt.Println()
	fmt.Println("优选 IP:", anycast)
	fmt.Println("设置带宽:", bandwidth, "Mbps")
	fmt.Println("实测带宽:", realBandwidth, "Mbps")
	fmt.Println("峰值速度:", max, "kB/s")
	fmt.Println("往返延迟:", avgms, "毫秒")
	fmt.Println("数据中心:", dataCenter)
	fmt.Println("总计用时:", int(endTime.Sub(startTime).Seconds()), "秒")
}

// cloudflareTest 核心测试逻辑
func cloudflareTest(ipType int, useTLS bool, taskNum int, speed int) (string, int, int, string) {
	downloadAllData()
	filename := dataPath("ips-v4.txt")
	if ipType == 6 {
		filename = dataPath("ips-v6.txt")
	}
	content, _ := getFileContent(filename)
	ipList := parseIPList(content)
	fmt.Printf("正在从 %d 个子网中随机生成 IP...\n", len(ipList))

	sampleSize := 100
	if len(ipList) < sampleSize {
		sampleSize = len(ipList)
	}

	var rttResults []RTTResult
	for {
		sampled := randomSample(ipList, sampleSize)

		var testIPs []string
		if ipType == 6 {
			testIPs = getRandomIPv6s(sampled)
		} else {
			testIPs = getRandomIPv4s(sampled)
		}

		fmt.Printf("已生成 %d 个测试 IP，开始 RTT 测试...\n", len(testIPs))

		rttResults = runRTTTest(testIPs, taskNum, useTLS)
		if len(rttResults) > 0 {
			break
		}
		fmt.Println("当前所有 IP 都存在 RTT 丢包，继续新的 RTT 测试...")
	}

	fmt.Println("待测速的 IP 地址")
	for _, r := range rttResults {
		fmt.Printf("%s 往返延迟 %d 毫秒\n", r.IP, r.LatencyMs)
	}

	// 速度测试
	for _, r := range rttResults {
		fmt.Println("正在测试", r.IP)
		speedPort := 80
		if useTLS {
			speedPort = 443
		}
		maxSpeed, tcpMs, dc := runSpeedTestSimple(r.IP, speedPort, useTLS)
		fmt.Printf("%s 峰值速度 %d kB/s", r.IP, maxSpeed)
		if dc != "" {
			fmt.Printf(", 数据中心=%s", lookupDataCenter(dc))
		}
		fmt.Println()

		if maxSpeed >= speed {
			if dc != "" {
				return r.IP, maxSpeed, tcpMs, lookupDataCenter(dc)
			}
			return r.IP, maxSpeed, tcpMs, dc
		}
	}

	return "", 0, 0, ""
}

// randomSample 从列表中随机抽取 n 个元素
func randomSample(list []string, n int) []string {
	shuffled := make([]string, len(list))
	copy(shuffled, list)
	randomMu.Lock()
	randomGenerator.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})
	randomMu.Unlock()
	if n > len(shuffled) {
		n = len(shuffled)
	}
	return shuffled[:n]
}

// RTTResult RTT 测试结果
type RTTResult struct {
	IP        string
	LatencyMs int
}

// runRTTTest 运行 RTT 测试（并发，带进度显示）
func runRTTTest(ipList []string, taskNum int, useTLS bool) []RTTResult {
	if len(ipList) < taskNum {
		taskNum = len(ipList)
	}

	var wg sync.WaitGroup
	resultChan := make(chan RTTResult, len(ipList))
	thread := make(chan struct{}, taskNum)
	var count int
	var mu sync.Mutex
	total := len(ipList)

	for _, ip := range ipList {
		wg.Add(1)
		thread <- struct{}{}
		go func(ip string) {
			defer func() {
				<-thread
				wg.Done()
				mu.Lock()
				count++
				current := count
				mu.Unlock()
				if current%10 == 0 || current == total {
					fmt.Printf("RTT 测试进度: %d/%d\n", current, total)
				}
			}()

			avgMs := testRTT(ip, useTLS)
			if avgMs > 0 {
				resultChan <- RTTResult{IP: ip, LatencyMs: avgMs}
			}
		}(ip)
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	var results []RTTResult
	for r := range resultChan {
		results = append(results, r)
	}

	// 按最小延迟排序
	sort.Slice(results, func(i, j int) bool {
		return results[i].LatencyMs < results[j].LatencyMs
	})

	fmt.Printf("RTT 测试完成，%d/%d 个 IP 有效\n", len(results), total)
	return results
}

// testRTT 测试单个 IP 的 RTT（TCP 连接 + 验证 CF-RAY）
// 连续 3 次取 TCP 连接时间，取平均延迟，中间任何一次失败直接丢弃
func testRTT(ip string, useTLS bool) int {
	port := 80
	if useTLS {
		port = 443
	}

	var totalMs int
	for range 3 {
		start := time.Now()
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)), 1*time.Second)
		if err != nil {
			return 0
		}
		tcpDuration := time.Since(start)

		conn.SetDeadline(start.Add(1 * time.Second))

		var rwc net.Conn = conn
		if useTLS {
			tlsConn := tls.Client(conn, &tls.Config{ServerName: "cloudflare.com", InsecureSkipVerify: true})
			if err := tlsConn.Handshake(); err != nil {
				conn.Close()
				return 0
			}
			rwc = tlsConn
		}

		reqStr := "GET / HTTP/1.1\r\nHost: cloudflare.com\r\nUser-Agent: Mozilla/5.0\r\nConnection: close\r\n\r\n"
		_, err = rwc.Write([]byte(reqStr))
		if err != nil {
			rwc.Close()
			return 0
		}

		reader := bufio.NewReader(rwc)
		resp, err := http.ReadResponse(reader, nil)
		rwc.Close()
		if err != nil {
			return 0
		}
		resp.Body.Close()

		if resp.Header.Get("CF-RAY") == "" {
			return 0
		}

		totalMs += int(tcpDuration.Milliseconds())
	}

	return totalMs / 3
}

// runSpeedTestSimple 简单速度测试，返回 (峰值速度 kB/s, TCP延迟ms, 三字码头)
func runSpeedTestSimple(ip string, port int, useTLS bool) (int, int, string) {
	var tcpMs int
	client := &http.Client{
		Transport: &http.Transport{
			Dial: func(network, addr string) (net.Conn, error) {
				start := time.Now()
				conn, err := net.Dial("tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
				if err == nil {
					tcpMs = int(time.Since(start).Milliseconds())
				}
				return conn, err
			},
		},
		Timeout: 10 * time.Second,
	}

	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	testURL := fmt.Sprintf("%s://%s/%s", scheme, net.JoinHostPort(speedTestDomain, strconv.Itoa(port)), speedTestFile)

	start := time.Now()
	resp, err := client.Get(testURL)
	if err != nil {
		return 0, 0, ""
	}
	defer resp.Body.Close()

	cfRay := resp.Header.Get("CF-RAY")
	dataCenter := extractDataCenter(cfRay)

	buf := make([]byte, 32*1024)
	var totalBytes int64
	maxSpeed := 0

	for {
		n, err := resp.Body.Read(buf)
		totalBytes += int64(n)
		if err != nil {
			break
		}

		elapsed := time.Since(start).Seconds()
		if elapsed > 0 {
			speedKB := int(float64(totalBytes) / 1024 / elapsed)
			if speedKB > maxSpeed {
				maxSpeed = speedKB
			}
		}
	}

	return maxSpeed, tcpMs, dataCenter
}

// extractDataCenter 从 CF-RAY 头提取三字码头
func extractDataCenter(cfRay string) string {
	if cfRay == "" {
		return ""
	}
	parts := strings.Split(cfRay, "-")
	if len(parts) < 2 {
		return ""
	}
	return strings.TrimSpace(parts[len(parts)-1])
}

// lookupDataCenter 查找数据中心名称
func lookupDataCenter(colo string) string {
	locationMu.RLock()
	loc := locationMap[colo]
	locationMu.RUnlock()

	if loc.City != "" {
		return loc.City
	}
	return colo
}

// runSingleSpeedTest 单 IP 测速
func runSingleSpeedTest(useTLS bool) {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Print("请输入需要测速的 IP: ")
	if !scanner.Scan() {
		return
	}
	ip := strings.TrimSpace(scanner.Text())

	defaultPort := 80
	if useTLS {
		defaultPort = 443
	}

	fmt.Printf("请输入需要测速的端口 (默认%d): ", defaultPort)
	var port int
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			port = defaultPort
		} else {
			port, err := strconv.Atoi(input)
			if err != nil || port <= 0 {
				fmt.Printf("输入无效，已使用默认端口 %d\n", defaultPort)
				port = defaultPort
			}
		}
	} else {
		port = defaultPort
	}

	fmt.Printf("正在测速 %s 端口 %d\n", ip, port)

	speedKB, tcpMs, dc := runSpeedTestSimple(ip, port, useTLS)
	if dc != "" {
		fmt.Printf("%s 平均速度 %d kB/s, TCP延迟 %dms, 数据中心=%s\n", ip, speedKB, tcpMs, lookupDataCenter(dc))
	} else {
		fmt.Printf("%s 平均速度 %d kB/s, TCP延迟 %dms\n", ip, speedKB, tcpMs)
	}
}

// clearCache 清空缓存，删除所有数据文件，下次运行重新下载
func clearCache() {
	for _, f := range []string{"locations.json", "ips-v4.txt", "ips-v6.txt", "url.txt"} {
		os.Remove(dataPath(f))
	}
	fmt.Println("缓存已清空，下次操作会自动重新下载数据")
}

// updateData 重新下载所有数据
func updateData() {
	fmt.Println("正在重新下载数据...")
	for _, f := range []string{"locations.json", "ips-v4.txt", "ips-v6.txt", "url.txt"} {
		os.Remove(dataPath(f))
	}
	initLocations()
}

// ----------------------- 工具函数 -----------------------

var (
	dataDir          string
	randomMu         sync.Mutex
	randomGenerator  = rand.New(rand.NewSource(time.Now().UnixNano()))
	locationMap      map[string]location
	locationMu       sync.RWMutex
	speedTestDomain  string
	speedTestFile    string
)

type location struct {
	Iata   string  `json:"iata"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	Cca2   string  `json:"cca2"`
	Region string  `json:"region"`
	City   string  `json:"city"`
}

func dataPath(name string) string {
	if dataDir == "" {
		return name
	}
	return filepath.Join(dataDir, name)
}

var downloadClient = &http.Client{Timeout: 30 * time.Second}

func getURLContent(targetURL string) (string, error) {
	resp, err := downloadClient.Get(targetURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func getFileContent(filename string) (string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func saveToFile(filename, content string) error {
	dir := filepath.Dir(filename)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	return os.WriteFile(filename, []byte(content), 0644)
}

func parseIPList(content string) []string {
	scanner := bufio.NewScanner(strings.NewReader(content))
	var ipList []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			ipList = append(ipList, line)
		}
	}
	return ipList
}

func nextRandomIntn(n int) int {
	randomMu.Lock()
	defer randomMu.Unlock()
	return randomGenerator.Intn(n)
}

func getRandomIPv4s(ipList []string) []string {
	var randomIPs []string
	for _, subnet := range ipList {
		subnet = strings.TrimSpace(subnet)
		if subnet == "" {
			continue
		}
		baseIP := strings.TrimSuffix(subnet, "/24")
		octets := strings.Split(baseIP, ".")
		if len(octets) >= 4 {
			octets[3] = fmt.Sprintf("%d", nextRandomIntn(256))
			randomIPs = append(randomIPs, strings.Join(octets, "."))
		}
	}
	return randomIPs
}

func getRandomIPv6s(ipList []string) []string {
	var randomIPs []string
	for _, subnet := range ipList {
		subnet = strings.TrimSpace(subnet)
		if subnet == "" {
			continue
		}
		baseIP := strings.TrimSuffix(subnet, "/48")
		sections := strings.Split(baseIP, ":")
		if len(sections) >= 3 {
			sections = sections[:3]
			for i := 3; i < 8; i++ {
				sections = append(sections, fmt.Sprintf("%x", nextRandomIntn(65536)))
			}
			randomIPs = append(randomIPs, strings.Join(sections, ":"))
		}
	}
	return randomIPs
}

// downloadAllData 确保所有数据文件存在，缺失则自动下载
func downloadAllData() {
	urlFilename := dataPath("url.txt")
	if _, err := os.Stat(urlFilename); os.IsNotExist(err) {
		fmt.Println("本地", urlFilename, "不存在，正在下载...")
		content, err := getURLContent("https://www.baipiao.eu.org/cloudflare/url")
		if err != nil {
			fmt.Println("下载测速 URL 失败")
		} else {
			saveToFile(urlFilename, content)
		}
	}

	content, _ := getFileContent(urlFilename)
	content = strings.TrimSpace(content)
	parts := strings.SplitN(content, "/", 2)
	if len(parts) == 2 {
		speedTestDomain = parts[0]
		speedTestFile = parts[1]
	} else {
		fmt.Println("测速 URL 格式异常")
	}

	for _, item := range []struct{ file, url string }{
		{"ips-v4.txt", "https://www.baipiao.eu.org/cloudflare/ips-v4"},
		{"ips-v6.txt", "https://www.baipiao.eu.org/cloudflare/ips-v6"},
	} {
		fp := dataPath(item.file)
		if _, err := os.Stat(fp); os.IsNotExist(err) {
			fmt.Println("本地", fp, "不存在，正在下载...")
			c, err := getURLContent(item.url)
			if err != nil {
				fmt.Println("下载 IP 列表失败:", err)
				continue
			}
			saveToFile(fp, c)
		}
	}

	fp := dataPath("locations.json")
	if _, err := os.Stat(fp); os.IsNotExist(err) {
		fmt.Println("本地", fp, "不存在，正在下载...")
		resp, err := downloadClient.Get("https://www.baipiao.eu.org/cloudflare/locations")
		if err != nil {
			fmt.Println("获取位置信息失败:", err)
			return
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
		resp.Body.Close()
		if err != nil {
			fmt.Println("读取响应内容失败:", err)
			return
		}
		saveToFile(fp, string(body))
	}
}

// initLocations 初始化数据中心位置信息
func initLocations() {
	downloadAllData()

	fp := dataPath("locations.json")
	body, err := os.ReadFile(fp)
	if err != nil {
		fmt.Println("读取位置文件失败:", err)
		return
	}

	var locations []location
	if err := json.Unmarshal(body, &locations); err != nil {
		fmt.Println("解析位置信息 JSON 失败:", err)
		return
	}

	loadedMap := make(map[string]location)
	for _, loc := range locations {
		loadedMap[loc.Iata] = loc
	}

	locationMu.Lock()
	locationMap = loadedMap
	locationMu.Unlock()

	fmt.Printf("已加载 %d 个数据中心位置信息\n", len(loadedMap))
}
