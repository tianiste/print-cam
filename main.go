package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/pion/webrtc/v4"
)

var peers sync.Map

func main() {
	http.HandleFunc("/", index)
	http.HandleFunc("/offer", offer)

	log.Println("open http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func index(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "index.html")
}

func offer(w http.ResponseWriter, r *http.Request) {
	var browserOffer webrtc.SessionDescription
	if err := json.NewDecoder(r.Body).Decode(&browserOffer); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	peerID := fmt.Sprintf("%p", pc)
	peers.Store(peerID, pc)

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Println("peer connection state:", state)

		switch state {
		case webrtc.PeerConnectionStateFailed,
			webrtc.PeerConnectionStateClosed,
			webrtc.PeerConnectionStateDisconnected:
			peers.Delete(peerID)
			if err := pc.Close(); err != nil {
				log.Println("close peer connection:", err)
			}
		}
	})

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Println("data channel opened:", dc.Label())

		dc.OnOpen(func() {
			log.Println("data channel ready")
			dc.SendText("hello from Go")
		})

		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			log.Println("browser says:", string(msg.Data))
			dc.SendText("Go received: " + string(msg.Data))
		})
	})

	if err := pc.SetRemoteDescription(browserOffer); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := pc.SetLocalDescription(answer); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	<-webrtc.GatheringCompletePromise(pc)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(pc.LocalDescription()); err != nil {
		peers.Delete(peerID)
		log.Println("write answer:", err)
		return
	}

	fmt.Println("WebRTC connection answered")
}
