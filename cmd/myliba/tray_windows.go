//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"fyne.io/systray"
)

const localAppURL = "http://127.0.0.1:4317"

func runWindowsTray(server *http.Server, hubs *hubProfiles) (bool, error) {
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return true, err
	}
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Myliba server stopped: %v", err)
			systray.Quit()
		}
	}()
	systray.Run(func() {
		systray.SetIcon(trayIcon())
		systray.SetTooltip("Myliba 正在运行")
		openItem := systray.AddMenuItem("打开 Myliba", "在浏览器中打开本地资源库")
		localItem := systray.AddMenuItem("本地服务：运行中", "本机文件服务正在运行")
		localItem.Disable()
		hubItem := systray.AddMenuItem("Hub：读取状态中", "当前 Hub 节点连接状态")
		hubItem.Disable()
		systray.AddSeparator()
		quitItem := systray.AddMenuItem("退出 Myliba", "关闭本地服务并退出")
		refreshStatus := func() {
			active, status := hubs.trayState()
			label := hubStatusLabel(active, status)
			hubItem.SetTitle("Hub：" + label)
			systray.SetTooltip("Myliba 正在运行 · Hub：" + label)
		}
		refreshStatus()
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-openItem.ClickedCh:
					if err := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", localAppURL).Start(); err != nil {
						log.Printf("open Myliba browser: %v", err)
					}
				case <-quitItem.ClickedCh:
					systray.Quit()
					return
				case <-ticker.C:
					refreshStatus()
				}
			}
		}()
	}, func() {
		systray.SetTooltip("Myliba 已退出")
		hubs.close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
		}
		<-serveDone
	})
	return true, nil
}

func hubStatusLabel(active bool, status string) string {
	if !active {
		return "未连接"
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "connected":
		return "已连接"
	case "connecting":
		return "连接中"
	case "auth_required":
		return "需要重新授权"
	case "disconnected":
		return "已断开"
	default:
		if status == "" || status == "disabled" {
			return "未连接"
		}
		return status
	}
}

func trayIcon() []byte {
	const size = 32
	icon := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(icon, icon.Bounds(), &image.Uniform{C: color.RGBA{R: 34, G: 112, B: 75, A: 255}}, image.Point{}, draw.Src)
	page := &image.Uniform{C: color.RGBA{R: 245, G: 250, B: 246, A: 255}}
	draw.Draw(icon, image.Rect(6, 7, 15, 25), page, image.Point{}, draw.Src)
	draw.Draw(icon, image.Rect(17, 7, 26, 25), page, image.Point{}, draw.Src)
	seam := &image.Uniform{C: color.RGBA{R: 34, G: 112, B: 75, A: 255}}
	draw.Draw(icon, image.Rect(15, 7, 17, 25), seam, image.Point{}, draw.Src)
	line := &image.Uniform{C: color.RGBA{R: 116, G: 166, B: 137, A: 255}}
	for _, y := range []int{12, 16, 20} {
		draw.Draw(icon, image.Rect(8, y, 13, y+1), line, image.Point{}, draw.Src)
		draw.Draw(icon, image.Rect(19, y, 24, y+1), line, image.Point{}, draw.Src)
	}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, icon); err != nil {
		return nil
	}
	data := pngData.Bytes()
	icoData := make([]byte, 22+len(data))
	binary.LittleEndian.PutUint16(icoData[2:4], 1)
	binary.LittleEndian.PutUint16(icoData[4:6], 1)
	icoData[6], icoData[7] = 0, 0
	binary.LittleEndian.PutUint16(icoData[10:12], 1)
	binary.LittleEndian.PutUint16(icoData[12:14], 32)
	binary.LittleEndian.PutUint32(icoData[14:18], uint32(len(data)))
	binary.LittleEndian.PutUint32(icoData[18:22], 22)
	copy(icoData[22:], data)
	return icoData
}
