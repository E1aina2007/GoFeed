<script setup lang="ts">
import { RouterLink } from 'vue-router'

import type { VideoItem } from '@/features/video/api'

defineProps<{
  video: VideoItem
  deletable?: boolean
}>()

defineEmits<{
  delete: [video: VideoItem]
}>()
</script>

<template>
  <article class="video-list-item">
    <RouterLink class="video-list-item__media" :to="{ name: 'video-detail', params: { id: video.id } }">
      <video :src="video.play_url" :poster="video.cover_url" muted playsinline preload="metadata"></video>
    </RouterLink>
    <div class="video-list-item__body">
      <RouterLink class="video-list-item__title" :to="{ name: 'video-detail', params: { id: video.id } }">
        {{ video.title }}
      </RouterLink>
      <p v-if="video.description" class="video-list-item__description">{{ video.description }}</p>
      <p class="video-list-item__meta">{{ video.play_original_name }} · {{ video.published_at }}</p>
      <button v-if="deletable" class="text-action text-action--danger" type="button" @click="$emit('delete', video)">
        删除视频
      </button>
    </div>
  </article>
</template>

<style scoped>
.video-list-item {
  display: grid;
  grid-template-columns: minmax(150px, 220px) minmax(0, 1fr);
  gap: 18px;
  border-bottom: 1px solid var(--border-subtle);
  padding: 18px 0;
}

.video-list-item__media {
  display: block;
  aspect-ratio: 16 / 10;
  overflow: hidden;
  background: var(--media-fallback);
}

.video-list-item__media video {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.video-list-item__body {
  min-width: 0;
}

.video-list-item__title {
  color: var(--ink-strong);
  font-size: 1.05rem;
  font-weight: 700;
  text-decoration: none;
}

.video-list-item__description,
.video-list-item__meta {
  color: var(--ink-muted);
  line-height: 1.5;
}

.video-list-item__description {
  margin: 10px 0 6px;
}

.video-list-item__meta {
  margin: 0;
  font-size: 0.82rem;
}

.text-action {
  margin-top: 12px;
  border: 0;
  padding: 0;
  color: var(--accent-strong);
  background: transparent;
  font-weight: 700;
  cursor: pointer;
}

.text-action--danger {
  color: #ae2c20;
}

@media (max-width: 640px) {
  .video-list-item {
    grid-template-columns: 120px minmax(0, 1fr);
    gap: 12px;
  }

  .video-list-item__meta {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
}
</style>
