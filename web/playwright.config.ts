import { defineConfig } from '@playwright/test'
export default defineConfig({
  testDir:'./tests',
  fullyParallel:false,
  workers:1,
  retries:0,
  reporter:'list',
  use:{baseURL:'http://127.0.0.1:18080',viewport:{width:1536,height:1024},browserName:'chromium'},
  webServer:{command:'python3 ../scripts/e2e_server.py',url:'http://127.0.0.1:18080/readyz',reuseExistingServer:false,timeout:120_000},
})
