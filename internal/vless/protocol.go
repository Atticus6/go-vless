// Package vless 实现 VLESS over WebSocket 协议解析与会话转发.
package vless

import (
	"encoding/binary"
	"fmt"
	"net"

	"github.com/atticus6/go-vless/internal/user"
	"github.com/google/uuid"
)

// VLESS 协议版本
const vlessVersion = 0

// VLESS 地址类型
const (
	atypIPv4   = 1
	atypDomain = 2
	atypIPv6   = 3
)

// VLESS 命令类型
const (
	cmdTCP = 1
	cmdUDP = 2
	cmdMux = 3
)

// parseRequest 解析 VLESS 请求, 并在用户表中校验 UUID, 返回请求者 ID.
// VLESS 协议格式:
// +----------+----------+----------+----------+----------+----------+----------+
// | Version  |  UUID    | Addon    | Command  | Port     | AddrType | Address  |
// | 1 byte   | 16 bytes | Variable | 1 byte   | 2 bytes  | 1 byte   | Variable |
// +----------+----------+----------+----------+----------+----------+----------+
func parseRequest(data []byte, users *user.Registry) (reqID uuid.UUID, addr string, command byte, payload []byte, err error) {
	if len(data) < 24 {
		return uuid.Nil, "", 0, nil, fmt.Errorf("data too short: %d", len(data))
	}

	// 版本检查
	version := data[0]
	if version != vlessVersion {
		return uuid.Nil, "", 0, nil, fmt.Errorf("unsupported version: %d", version)
	}

	// UUID 验证
	reqID, err = uuid.FromBytes(data[1:17])
	if err != nil {
		return uuid.Nil, "", 0, nil, fmt.Errorf("invalid UUID: %v", err)
	}
	if users == nil || !users.Valid(reqID) {
		return uuid.Nil, "", 0, nil, fmt.Errorf("UUID mismatch")
	}

	// Addon 长度
	addonLen := data[17]
	offset := 18 + int(addonLen)

	if len(data) < offset+4 {
		return uuid.Nil, "", 0, nil, fmt.Errorf("data too short for command")
	}

	// 命令
	command = data[offset]
	offset++

	// 端口 (big-endian)
	port := binary.BigEndian.Uint16(data[offset : offset+2])
	offset += 2

	// 地址类型
	addrType := data[offset]
	offset++

	var host string
	switch addrType {
	case atypIPv4:
		if len(data) < offset+4 {
			return uuid.Nil, "", 0, nil, fmt.Errorf("data too short for IPv4")
		}
		host = net.IP(data[offset : offset+4]).String()
		offset += 4
	case atypDomain:
		if len(data) < offset+1 {
			return uuid.Nil, "", 0, nil, fmt.Errorf("data too short for domain length")
		}
		domainLen := int(data[offset])
		offset++
		if len(data) < offset+domainLen {
			return uuid.Nil, "", 0, nil, fmt.Errorf("data too short for domain")
		}
		host = string(data[offset : offset+domainLen])
		offset += domainLen
	case atypIPv6:
		if len(data) < offset+16 {
			return uuid.Nil, "", 0, nil, fmt.Errorf("data too short for IPv6")
		}
		host = net.IP(data[offset : offset+16]).String()
		offset += 16
	default:
		return uuid.Nil, "", 0, nil, fmt.Errorf("unsupported address type: %d", addrType)
	}

	addr = fmt.Sprintf("%s:%d", host, port)

	// 剩余数据作为 payload
	if offset < len(data) {
		payload = data[offset:]
	}

	return reqID, addr, command, payload, nil
}
