<script setup>
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { mutate, request } from '../api'

const router = useRouter()
const cameras = ref([])
const name = ref('')
const status = ref('')
const busy = ref(false)
const editing = ref({})

async function load() {
  try {
    await request('/api/auth/me')
    cameras.value = await request('/api/cameras')
    editing.value = Object.fromEntries(cameras.value.map((camera) => [camera.id, camera.name]))
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

async function renameCamera(camera) {
  const nextName = (editing.value[camera.id] || '').trim()
  if (!nextName || nextName === camera.name) return
  busy.value = true
  status.value = ''
  try {
    await mutate(`/api/cameras/${camera.id}`, { name: nextName })
    await load()
    status.value = 'Camera updated.'
  } catch (err) {
    status.value = err.message
  } finally {
    busy.value = false
  }
}

async function deleteCamera(camera) {
  if (!confirm(`Delete ${camera.name}?`)) return
  busy.value = true
  status.value = ''
  try {
    await mutate(`/api/cameras/${camera.id}`, undefined, 'DELETE')
    await load()
    status.value = 'Camera deleted.'
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
          <form class="space-y-3" @submit.prevent="renameCamera(camera)">
            <label class="block text-sm font-medium text-ink">
              Camera name
              <input v-model="editing[camera.id]" class="control mt-2" required />
            </label>
            <div class="flex gap-2">
              <button class="button-secondary" :disabled="busy || editing[camera.id] === camera.name">Save</button>
              <button class="button-secondary border-red-200 text-red-700 hover:border-red-400 hover:text-red-800" type="button" :disabled="busy" @click="deleteCamera(camera)">Delete</button>
            </div>
          </form>
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
