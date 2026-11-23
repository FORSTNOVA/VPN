package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

func (a *driverInstallOracle) pe() error {
	r := a.call("pe-header", "", "", 0)
	body := r.Bytes
	size := r.Number
	if size < 0x40 || len(body) < 0x40 || !bytes.HasPrefix(body, []byte("MZ")) {
		return driverBadArch
	}
	offset := int(uint32(body[0x3C]) | uint32(body[0x3D])<<8 | uint32(body[0x3E])<<16 | uint32(body[0x3F])<<24)
	if offset <= 0 || int64(offset)+6 > size {
		return driverBadArch
	}
	part := a.call("pe-at", "", "", int64(offset)).Bytes
	if len(part) != 6 || !bytes.Equal(part[:4], []byte("PE\x00\x00")) {
		return driverBadArch
	}
	machine := uint16(part[4]) | uint16(part[5])<<8
	if machine != 0x8664 {
		return fmt.Errorf("%w（machine 0x%04x）", driverBadArch, machine)
	}
	return nil
}
func (a *driverInstallOracle) signature(path string) (string, error) {
	r := a.call("signature", path, "", 0)
	out, err := []byte(r.Text), r.Err
	if err != nil {
		return "", fmt.Errorf("无法校验 wintun.dll 的签名：%s", strings.TrimSpace(string(out)))
	}
	text := strings.TrimSpace(string(out))
	if !strings.HasPrefix(text, "valid:") {
		return "", fmt.Errorf("%w（签名状态：%s）", driverBadWintun, strings.TrimPrefix(text, "invalid:"))
	}
	subject := strings.TrimPrefix(text, "valid:")
	if !strings.Contains(subject, driverWintunSigner) {
		return subject, fmt.Errorf("%w（签发者：%s）", driverBadWintun, subject)
	}
	return driverWintunSigner, nil
}
func (a *driverInstallOracle) download() error {
	r := a.call("http-open", driverWintunURL, "", 0)
	err := r.Err
	if err != nil {
		return errors.New("无法下载 wintun.dll，请检查网络，或在设置中指定本地文件")
	}
	defer a.call("http-close", "", "", 0)
	if int(r.Number) != http.StatusOK {
		return fmt.Errorf("wintun 下载返回 HTTP %d", int(r.Number))
	}
	err = a.call("http-read", "", "", 8*1024*1024).Err
	if err != nil {
		return errors.New("wintun 下载中断")
	}
	if a.call("hash-zip", "", "", 0).Text != driverWintunZipSHA256 {
		return fmt.Errorf("%w（压缩包哈希不匹配）", driverBadWintun)
	}
	archive := a.call("zip-open", "", "", 0)
	err = archive.Err
	if err != nil {
		return fmt.Errorf("%w（压缩包无法解析）", driverBadWintun)
	}
	for index, name := range archive.Names {
		if name != driverWintunEntry {
			continue
		}
		err := a.call("zip-entry-open", "", "", int64(index)).Err
		if err != nil {
			return fmt.Errorf("%w（压缩包读取失败）", driverBadWintun)
		}
		defer a.call("zip-entry-close", "", "", 0)
		err = a.call("zip-entry-read", "", "", 2*1024*1024).Err
		if err != nil {
			return fmt.Errorf("%w（压缩包读取失败）", driverBadWintun)
		}
		if a.call("hash-dll", "", "", 0).Text != driverWintunDLLSHA256 {
			return fmt.Errorf("%w（dll 哈希不匹配）", driverBadWintun)
		}
		if err := a.pe(); err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("%w（压缩包内没有 amd64 库）", driverBadWintun)
}

func (a *driverInstallOracle) install() driverInstallResult {
	decoded := a.call("decode", "", "", 0)
	if decoded.Err != nil && !decoded.Flag {
		return driverInstallResult{Status: 400, Err: errors.New("invalid request body")}
	}
	source := strings.TrimSpace(decoded.Text)
	detail := ""
	if source != "" {
		err := a.call("read-local", source, "", 0).Err
		if err != nil {
			return driverInstallResult{Status: http.StatusBadRequest, Err: errors.New("无法读取指定的 wintun.dll")}
		}
		if err := a.pe(); err != nil {
			return driverInstallResult{Status: http.StatusBadRequest, Err: err}
		}
		detail = "已使用你指定的文件"
	} else {
		err := a.download()
		if err != nil {
			return driverInstallResult{Status: http.StatusBadGateway, Err: err}
		}
		detail = fmt.Sprintf("已从官方源安装 wintun %s", driverWintunVersion)
	}

	a.call("lock", "", "", 0)
	target := a.call("target", "", "", 0).Text
	tempPath := target + ".tmp"
	if err := a.call("write-temp", tempPath, "", 0600).Err; err != nil {
		return driverInstallResult{Status: http.StatusInternalServerError, Err: errors.New("无法写入 wintun.dll")}
	}
	signer, err := a.signature(tempPath)
	if err != nil {
		a.call("remove-temp", tempPath, "", 0)
		return driverInstallResult{Status: http.StatusBadGateway, Err: err}
	}
	if err := a.call("rename", tempPath, target, 0).Err; err != nil {
		a.call("remove-temp", tempPath, "", 0)
		return driverInstallResult{Status: http.StatusInternalServerError, Err: errors.New("无法安装 wintun.dll")}
	}
	a.call("log", target, signer, 0)
	status := a.status()
	status.Detail = detail
	return driverInstallResult{Status: 200, Data: map[string]any{"ok": true, "wintun": status, "available": a.available()}}
}

func (a *driverInstallOracle) status() wintunStatus {
	target := a.call("target", "", "", 0).Text
	s := wintunStatus{Path: target}
	r := a.call("stat", target, "", 0)
	if r.Err != nil || r.Flag {
		s.Detail = tunReportNotInstalled
		return s
	}
	s.Installed = true
	s.Version = driverWintunVersion
	s.Signer = driverWintunSigner
	if h := a.call("hash-file", target, "", 0); h.Err == nil {
		s.SHA256 = h.Text
	}
	return s
}
func (a *driverInstallOracle) available() bool {
	if !a.call("elevated", "", "", 0).Flag {
		return false
	}
	return a.status().Installed
}
func runGoDriverInstall(q driverInstallRequest, fx driverInstallEffects) driverInstallResult {
	a := driverInstallOracle{fx: fx}
	switch q.Op {
	case "install":
		return a.install()
	case "download":
		return driverInstallResult{Err: a.download()}
	case "pe":
		return driverInstallResult{Err: a.pe()}
	case "signature":
		v, e := a.signature(q.Path)
		return driverInstallResult{Text: v, Err: e}
	default:
		panic("unknown driver install operation")
	}
}
