// Package sysutil 启动前的系统级尽力而为优化 (BBR).
package sysutil

import (
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// EnsureBBR 启动前检查 BBR, 非容器且未开启时尝试开启.
// 设计原则: 尽力而为, 任何失败只打 WARN, 绝不阻塞启动.
func EnsureBBR() {
	if runtime.GOOS != "linux" {
		return
	}
	// Vercel 等 serverless 环境无权改内核参数, 直接跳过.
	if os.Getenv("VERCEL") != "" {
		return
	}
	// 显式退出: SKIP_BBR=1/true
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("SKIP_BBR"))); v == "1" || v == "true" || v == "yes" {
		return
	}
	if isContainer() {
		return
	}
	cur := readSysctlTrim("/proc/sys/net/ipv4/tcp_congestion_control")
	if cur == "bbr" {
		return
	}
	if os.Geteuid() != 0 {
		log.Printf("[WARN] BBR not enabled (current=%q), need root to enable, skip", cur)
		return
	}
	avail := readSysctlTrim("/proc/sys/net/ipv4/tcp_available_congestion_control")
	if !strings.Contains(" "+avail+" ", " bbr ") {
		// 内核有模块但没加载时尝试 modprobe
		if out, err := exec.Command("modprobe", "tcp_bbr").CombinedOutput(); err != nil {
			log.Printf("[WARN] BBR not available (available=%q), modprobe tcp_bbr failed: %v %s. Need kernel >= 4.9", avail, err, strings.TrimSpace(string(out)))
			return
		}
		avail = readSysctlTrim("/proc/sys/net/ipv4/tcp_available_congestion_control")
		if !strings.Contains(" "+avail+" ", " bbr ") {
			log.Printf("[WARN] BBR still unavailable after modprobe (available=%q)", avail)
			return
		}
	}
	// 先切 qdisc 再切拥塞算法, BBR 要求 fq
	if out, err := exec.Command("sysctl", "-w", "net.core.default_qdisc=fq").CombinedOutput(); err != nil {
		log.Printf("[WARN] Enable BBR failed at default_qdisc: %v %s", err, strings.TrimSpace(string(out)))
		return
	}
	if out, err := exec.Command("sysctl", "-w", "net.ipv4.tcp_congestion_control=bbr").CombinedOutput(); err != nil {
		log.Printf("[WARN] Enable BBR failed at tcp_congestion_control: %v %s", err, strings.TrimSpace(string(out)))
		return
	}
	if cur2 := readSysctlTrim("/proc/sys/net/ipv4/tcp_congestion_control"); cur2 != "bbr" {
		log.Printf("[WARN] BBR sysctl applied but still not active (current=%q)", cur2)
		return
	}
	log.Printf("[INFO] BBR enabled (was %q)", cur)
	persistBBR()
}

// persistBBR 持久化到 /etc/sysctl.d, 失败只告警
func persistBBR() {
	const path = "/etc/sysctl.d/99-xchick-bbr.conf"
	const content = "net.core.default_qdisc = fq\nnet.ipv4.tcp_congestion_control = bbr\n"
	if old, err := os.ReadFile(path); err == nil && string(old) == content {
		return
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		log.Printf("[WARN] BBR active but persist failed (%s): %v", path, err)
	}
}

// isContainer 判断是否跑在容器内, 容器内一般无权改宿主机 sysctl, 直接跳过
func isContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	if v := strings.TrimSpace(os.Getenv("container")); v != "" {
		return true
	}
	// k8s / docker / containerd / podman / lxc 的 cgroup 痕迹
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(data)
		if strings.Contains(s, "docker") || strings.Contains(s, "kubepods") ||
			strings.Contains(s, "containerd") || strings.Contains(s, "lxc") ||
			strings.Contains(s, "podman") || strings.Contains(s, "crio") {
			return true
		}
	}
	return false
}

func readSysctlTrim(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
