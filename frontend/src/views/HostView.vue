<script setup>
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { mutate, request, wsBase } from '../api'

const route = useRoute()
const router = useRouter()
const video = ref(null)
const status = ref('Starting camera...')
const viewerCount = ref(0)
const peers = new Map()
let stream
let socket
let iceServers = []
let stopped = false
let reconnectTimer
let reconnectAttempt = 0

async function start() {
  try {
    await request('/api/auth/me')
    const turn = await mutate(`/api/cameras/${route.params.id}/turn-credentials`)
    iceServers = turn.iceServers || []
    stream = await navigator.mediaDevices.getUserMedia({ video: true, audio: false })
    video.value.srcObject = stream
    connectSocket()
  } catch (err) {
    status.value = err.message
    if (err.message.includes('login')) router.push('/login')
  }
}

function connectSocket() {
  clearTimeout(reconnectTimer)
  closePeers()
  if (socket) {
    socket.onclose = null
    socket.close()
  }
  socket = new WebSocket(`${wsBase()}/ws/host/${route.params.id}`)
  socket.onopen = () => {
    status.value = 'Signaling connected. Waiting for host confirmation.'
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
  closePeers()
  reconnectAttempt += 1
  const delay = Math.min(1000 * reconnectAttempt, 5000)
  status.value = `Signaling disconnected. Reconnecting in ${Math.round(delay / 1000)}s.`
  reconnectTimer = setTimeout(connectSocket, delay)
}

async function onMessage(event) {
  try {
    const msg = JSON.parse(event.data)
    if (msg.type === 'host-ready') {
      reconnectAttempt = 0
      status.value = 'Host online. Waiting for viewers.'
    }
    if (msg.type === 'viewer-join') await join(msg.viewerId)
    if (msg.type === 'answer') await peers.get(msg.viewerId)?.setRemoteDescription(msg.sdp)
    if (msg.type === 'ice-candidate' && msg.candidate) await peers.get(msg.viewerId)?.addIceCandidate(msg.candidate)
    if (msg.type === 'viewer-left') closePeer(msg.viewerId)
    if (msg.type === 'error') status.value = msg.error
  } catch (err) {
    status.value = err.message
  }
}

async function join(id) {
  closePeer(id)
  const pc = new RTCPeerConnection({ iceServers })
  peers.set(id, pc)
  viewerCount.value = peers.size
  stream.getTracks().forEach((track) => pc.addTrack(track, stream))
  pc.onicecandidate = (event) => {
    if (event.candidate) sendSignal({ type: 'ice-candidate', viewerId: id, candidate: event.candidate })
  }
  pc.onconnectionstatechange = () => {
    if (['failed', 'disconnected', 'closed'].includes(pc.connectionState)) closePeer(id)
  }
  const offer = await pc.createOffer()
  await pc.setLocalDescription(offer)
  sendSignal({ type: 'offer', viewerId: id, sdp: pc.localDescription })
  status.value = `${peers.size} viewer(s) connected`
}

function sendSignal(message) {
  if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify(message))
}

function closePeer(id) {
  peers.get(id)?.close()
  peers.delete(id)
  viewerCount.value = peers.size
  status.value = peers.size === 0 ? 'Host online. Waiting for viewers.' : `${peers.size} viewer(s) connected`
}

function closePeers() {
  for (const pc of peers.values()) pc.close()
  peers.clear()
  viewerCount.value = 0
}

onMounted(start)
onBeforeUnmount(() => {
  stopped = true
  clearTimeout(reconnectTimer)
  closePeers()
  socket?.close()
  stream?.getTracks().forEach((track) => track.stop())
})
</script>

<template>
  <main class="min-h-screen bg-paper px-5 py-8">
    <section class="mx-auto max-w-5xl">
      <header class="flex flex-col gap-3 border-b border-line pb-5 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <p class="text-sm font-medium text-moss">Host</p>
          <h1 class="mt-1 break-all text-2xl font-semibold text-ink">{{ route.params.id }}</h1>
        </div>
        <router-link class="button-secondary" to="/cameras">Cameras</router-link>
      </header>

      <section class="mt-6 overflow-hidden rounded-lg bg-black">
        <video ref="video" class="aspect-video h-full w-full object-contain" autoplay muted playsinline></video>
      </section>

      <div class="mt-5 flex flex-col gap-2 text-sm text-ink/70 sm:flex-row sm:items-center sm:justify-between">
        <p>{{ status }}</p>
        <p>{{ viewerCount }} viewer(s)</p>
      </div>
    </section>
  </main>
</template>
