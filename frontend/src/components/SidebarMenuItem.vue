<script setup lang="ts">
import type { NavigationItem } from '@/navigation'

interface SidebarMenuItemProps {
  /** Functional menu node rendered at the current hierarchy level. */
  item: NavigationItem
}

defineProps<SidebarMenuItemProps>()
</script>

<template>
  <el-sub-menu
    v-if="item.children?.length"
    :index="item.key"
    popper-class="sidebar-menu-popper"
  >
    <template #title>
      <component :is="item.icon" v-if="item.icon" class="menu-item-icon" />
      <span>{{ item.label }}</span>
    </template>

    <SidebarMenuItem
      v-for="child in item.children"
      :key="child.key"
      :item="child"
    />
  </el-sub-menu>

  <el-menu-item
    v-else
    :index="item.path ?? item.key"
    :route="item.path"
  >
    <component :is="item.icon" v-if="item.icon" class="menu-item-icon" />
    <span>{{ item.label }}</span>
  </el-menu-item>
</template>

<style scoped>
.menu-item-icon {
  width: 16px;
  height: 16px;
  flex-shrink: 0;
}

</style>
