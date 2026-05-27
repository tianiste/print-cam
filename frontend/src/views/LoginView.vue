<script setup>
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { request } from '../api'

const router = useRouter()
const email = ref('admin@example.com')
const password = ref('')
const code = ref('')
const step = ref('password')
const status = ref('')
const busy = ref(false)

async function login() {
  busy.value = true
  status.value = ''
  try {
    await request('/api/auth/login', {
      method: 'POST',
      body: JSON.stringify({ email: email.value, password: password.value })
    })
    step.value = 'totp'
    status.value = 'Enter authenticator code.'
  } catch (err) {
    status.value = err.message
  } finally {
    busy.value = false
  }
}

async function verify() {
  busy.value = true
  status.value = ''
  try {
    await request('/api/auth/totp/verify', {
      method: 'POST',
      body: JSON.stringify({ code: code.value })
    })
    router.push('/cameras')
  } catch (err) {
    status.value = err.message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main class="grid min-h-screen place-items-center bg-paper px-5 py-10">
    <section class="w-full max-w-sm">
      <p class="mb-3 text-sm font-medium text-moss">Print Cam</p>
      <h1 class="text-3xl font-semibold tracking-tight text-ink">Secure camera access</h1>
      <p class="mt-2 text-sm text-ink/65">Sign in, then open host or viewer for owned cameras.</p>

      <form v-if="step === 'password'" class="panel mt-7 space-y-4" @submit.prevent="login">
        <label class="block text-sm font-medium text-ink">
          Email
          <input v-model="email" class="control mt-2" type="email" autocomplete="username" required />
        </label>
        <label class="block text-sm font-medium text-ink">
          Password
          <input v-model="password" class="control mt-2" type="password" autocomplete="current-password" required />
        </label>
        <button class="button w-full" :disabled="busy">Continue</button>
      </form>

      <form v-else class="panel mt-7 space-y-4" @submit.prevent="verify">
        <label class="block text-sm font-medium text-ink">
          Authenticator code
          <input v-model="code" class="control mt-2" inputmode="numeric" autocomplete="one-time-code" required />
        </label>
        <button class="button w-full" :disabled="busy">Sign in</button>
      </form>

      <p class="mt-4 min-h-5 text-sm text-ink/70">{{ status }}</p>
    </section>
  </main>
</template>
