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

async function start() {
  try {
    await request('/api/auth/me')
    const turn = await mutate(`/api/cameras/${route.params.id}/turn-credentials`)
    iceServers = turn.iceServers || []
    pc = new RTCPeerConnection({ iceServers })
    pc.ontrack = (event) => {
      video.value.srcObject = event.streams[0]
      status.value = 'Live'
    }
    pc.onicecandidate = (event) => {
      if (event.candidate) socket.send(JSON.stringify({ type: 'ice-candidate', candidate: event.candidate }))
    }
    pc.onconnectionstatechange = () => {
      if (pc.connectionState) status.value = pc.connectionState
    }
    socket = new WebSocket(`${wsBase()}/ws/view/${route.params.id}`)
    socket.onmessage = onMessage
    socket.onclose = () => {
      if (status.value !== 'Live') status.value = 'Disconnected.'
    }
  } catch (err) {
    status.value = err.message
    if (err.message.includes('login')) router.push('/login')
  }
}

async function onMessage(event) {
  const msg = JSON.parse(event.data)
  if (msg.type === 'offer') {
    await pc.setRemoteDescription(msg.sdp)
    const answer = await pc.createAnswer()
    await pc.setLocalDescription(answer)
    socket.send(JSON.stringify({ type: 'answer', sdp: pc.localDescription }))
  }
  if (msg.type === 'ice-candidate' && msg.candidate) await pc.addIceCandidate(msg.candidate)
  if (msg.type === 'host-left') status.value = 'Host tab is offline.'
  if (msg.type === 'error') status.value = msg.error
}

onMounted(start)
onBeforeUnmount(() => {
  socket?.close()
  pc?.close()
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
