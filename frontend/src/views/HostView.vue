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

async function start() {
  try {
    await request('/api/auth/me')
    const turn = await mutate(`/api/cameras/${route.params.id}/turn-credentials`)
    iceServers = turn.iceServers || []
    stream = await navigator.mediaDevices.getUserMedia({ video: true, audio: false })
    video.value.srcObject = stream
    socket = new WebSocket(`${wsBase()}/ws/host/${route.params.id}`)
    socket.onmessage = onMessage
    socket.onclose = () => {
      status.value = 'Host disconnected.'
    }
  } catch (err) {
    status.value = err.message
    if (err.message.includes('login')) router.push('/login')
  }
}

async function onMessage(event) {
  const msg = JSON.parse(event.data)
  if (msg.type === 'host-ready') status.value = 'Host online. Waiting for viewers.'
  if (msg.type === 'viewer-join') await join(msg.viewerId)
  if (msg.type === 'answer') await peers.get(msg.viewerId)?.setRemoteDescription(msg.sdp)
  if (msg.type === 'ice-candidate' && msg.candidate) await peers.get(msg.viewerId)?.addIceCandidate(msg.candidate)
  if (msg.type === 'viewer-left') {
    peers.get(msg.viewerId)?.close()
    peers.delete(msg.viewerId)
    viewerCount.value = peers.size
    status.value = `${peers.size} viewer(s) connected`
  }
  if (msg.type === 'error') status.value = msg.error
}

async function join(id) {
  const pc = new RTCPeerConnection({ iceServers })
  peers.set(id, pc)
  viewerCount.value = peers.size
  stream.getTracks().forEach((track) => pc.addTrack(track, stream))
  pc.onicecandidate = (event) => {
    if (event.candidate) socket.send(JSON.stringify({ type: 'ice-candidate', viewerId: id, candidate: event.candidate }))
  }
  const offer = await pc.createOffer()
  await pc.setLocalDescription(offer)
  socket.send(JSON.stringify({ type: 'offer', viewerId: id, sdp: pc.localDescription }))
  status.value = `${peers.size} viewer(s) connected`
}

onMounted(start)
onBeforeUnmount(() => {
  for (const pc of peers.values()) pc.close()
  peers.clear()
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
