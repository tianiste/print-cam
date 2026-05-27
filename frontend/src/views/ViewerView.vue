<script setup>
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { mutate, request, wsBase } from '../api'

const route = useRoute()
const router = useRouter()
const video = ref(null)
const status = ref('Connecting...')
let socket
let pc
let iceServers = []
let stopped = false
let reconnectTimer
let reconnectAttempt = 0

async function start() {
  try {
    await request('/api/auth/me')
    const turn = await mutate(`/api/cameras/${route.params.id}/turn-credentials`)
    iceServers = turn.iceServers || []
    connectSocket()
  } catch (err) {
    status.value = err.message
    if (err.message.includes('login')) router.push('/login')
  }
}

function connectSocket() {
  clearTimeout(reconnectTimer)
  resetPeer()
  if (socket) {
    socket.onclose = null
    socket.close()
  }
  socket = new WebSocket(`${wsBase()}/ws/view/${route.params.id}`)
  socket.onopen = () => {
    status.value = 'Connected to signaling. Waiting for host.'
  }
  socket.onmessage = onMessage
  socket.onerror = () => {
    socket?.close()
  }
  socket.onclose = () => {
    if (stopped) return
    scheduleReconnect()
  }
}

function scheduleReconnect() {
  closePeer()
  reconnectAttempt += 1
  const delay = Math.min(1000 * reconnectAttempt, 5000)
  status.value = `Disconnected. Reconnecting in ${Math.round(delay / 1000)}s.`
  reconnectTimer = setTimeout(connectSocket, delay)
}

function resetPeer() {
  closePeer()
  const peer = new RTCPeerConnection({ iceServers })
  pc = peer
  peer.ontrack = (event) => {
    reconnectAttempt = 0
    video.value.srcObject = event.streams[0]
    status.value = 'Live'
  }
  peer.onicecandidate = (event) => {
    if (event.candidate) sendSignal({ type: 'ice-candidate', candidate: event.candidate })
  }
  peer.onconnectionstatechange = () => {
    if (peer !== pc) return
    if (peer.connectionState === 'connected') status.value = 'Live'
    if (peer.connectionState === 'failed' || peer.connectionState === 'disconnected') socket?.close()
  }
}

async function onMessage(event) {
  try {
    const msg = JSON.parse(event.data)
    if (msg.type === 'offer') {
      await pc.setRemoteDescription(msg.sdp)
      const answer = await pc.createAnswer()
      await pc.setLocalDescription(answer)
      sendSignal({ type: 'answer', sdp: pc.localDescription })
    }
    if (msg.type === 'ice-candidate' && msg.candidate) await pc.addIceCandidate(msg.candidate)
    if (msg.type === 'host-left') {
      closePeer()
      status.value = 'Host tab is offline.'
    }
    if (msg.type === 'error') status.value = msg.error
  } catch (err) {
    status.value = err.message
  }
}

function sendSignal(message) {
  if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify(message))
}

function closePeer() {
  pc?.close()
  pc = undefined
  if (video.value) video.value.srcObject = null
}

onMounted(start)
onBeforeUnmount(() => {
  stopped = true
  clearTimeout(reconnectTimer)
  socket?.close()
  closePeer()
})
</script>

<template>
  <main class="min-h-screen bg-paper px-5 py-8">
    <section class="mx-auto max-w-5xl">
      <header class="flex flex-col gap-3 border-b border-line pb-5 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <p class="text-sm font-medium text-moss">Viewer</p>
          <h1 class="mt-1 break-all text-2xl font-semibold text-ink">{{ route.params.id }}</h1>
        </div>
        <router-link class="button-secondary" to="/cameras">Cameras</router-link>
      </header>

      <section class="mt-6 overflow-hidden rounded-lg bg-black">
        <video ref="video" class="aspect-video h-full w-full object-contain" autoplay playsinline controls></video>
      </section>

      <p class="mt-5 text-sm text-ink/70">{{ status }}</p>
    </section>
  </main>
</template>
