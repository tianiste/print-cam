<script setup>
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { mutate, request } from '../api'

const router = useRouter()
const cameras = ref([])
const name = ref('')
const status = ref('')
const busy = ref(false)

async function load() {
  try {
    await request('/api/auth/me')
    cameras.value = await request('/api/cameras')
  } catch (err) {
    router.push('/login')
  }
}

async function addCamera() {
  if (!name.value.trim()) return
  busy.value = true
  status.value = ''
  try {
    await mutate('/api/cameras', { name: name.value.trim() })
    name.value = ''
    await load()
  } catch (err) {
    status.value = err.message
  } finally {
    busy.value = false
  }
}

async function logout() {
  try {
    await mutate('/api/auth/logout')
  } finally {
    router.push('/login')
  }
}

onMounted(load)
</script>

<template>
  <main class="min-h-screen bg-paper px-5 py-8">
    <section class="mx-auto max-w-5xl">
      <header class="flex flex-col gap-4 border-b border-line pb-6 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <p class="text-sm font-medium text-moss">Print Cam</p>
          <h1 class="mt-1 text-3xl font-semibold tracking-tight text-ink">Cameras</h1>
        </div>
        <button class="button-secondary" @click="logout">Log out</button>
      </header>

      <form class="mt-6 flex flex-col gap-3 sm:flex-row" @submit.prevent="addCamera">
        <input v-model="name" class="control sm:max-w-sm" placeholder="Camera name" required />
        <button class="button sm:w-auto" :disabled="busy">Add camera</button>
      </form>
      <p class="mt-3 min-h-5 text-sm text-ink/65">{{ status }}</p>

      <section class="mt-7 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <article v-for="camera in cameras" :key="camera.id" class="panel">
          <h2 class="text-lg font-semibold text-ink">{{ camera.name }}</h2>
          <p class="mt-2 break-all text-xs text-ink/50">{{ camera.id }}</p>
          <div class="mt-5 flex gap-2">
            <router-link class="button" :to="`/cameras/${camera.id}/host`">Host</router-link>
            <router-link class="button-secondary" :to="`/cameras/${camera.id}/view`">View</router-link>
          </div>
        </article>
      </section>

      <p v-if="cameras.length === 0" class="mt-12 text-sm text-ink/60">No cameras yet.</p>
    </section>
  </main>
</template>
