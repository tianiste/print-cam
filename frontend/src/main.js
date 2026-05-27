import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import './style.css'
import LoginView from './views/LoginView.vue'
import CamerasView from './views/CamerasView.vue'
import HostView from './views/HostView.vue'
import ViewerView from './views/ViewerView.vue'

const routes = [
  { path: '/', redirect: '/cameras' },
  { path: '/login', component: LoginView },
  { path: '/cameras', component: CamerasView },
  { path: '/cameras/:id/host', component: HostView },
  { path: '/cameras/:id/view', component: ViewerView }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

createApp(App).use(router).mount('#app')
