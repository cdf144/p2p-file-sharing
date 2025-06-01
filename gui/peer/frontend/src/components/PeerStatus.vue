<script lang="ts" setup>
const props = defineProps<{
    statusMessage: string;
    isServing: boolean;
    shareDir: string;
}>();
</script>

<template>
    <div class="rounded-lg bg-gray-700 p-4 shadow">
        <h3 class="font-semibold">Status:</h3>
        <p class="break-all text-gray-300">{{ statusMessage }}</p>
        <p v-if="isServing" class="text-green-400">Peer is serving files from: {{ shareDir || 'not set' }}</p>
    </div>
</template>
