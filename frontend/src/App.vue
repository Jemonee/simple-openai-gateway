<script setup>
import AppShell from './components/AppShell.vue'
</script>

<template>
  <RouterView v-slot="{ Component }">
    <AppShell>
      <component :is="Component" />
    </AppShell>
  </RouterView>
</template>
