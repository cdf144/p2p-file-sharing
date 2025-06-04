<script lang="ts" setup>
import { computed } from 'vue';
import { protocol } from '../../wailsjs/go/models';

const props = defineProps<{
    isQuerying: boolean;
    queryError: string;
    lastQueryTime: Date | null;
    networkFiles: protocol.FileMeta[];
    allNetworkFilesCount: number;
}>();

const emit = defineEmits(['discover-peers']);

const networkFilesCount = computed(() => props.networkFiles.length);

function discoverPeers() {
    emit('discover-peers');
}
</script>

<template>
    <div class="space-y-4 rounded-lg bg-gray-700 p-6 shadow-lg">
        <div class="flex items-center justify-between">
            <h2 class="text-xl font-semibold">Network Discovery</h2>
            <button
                @click="discoverPeers"
                class="focus:ring-opacity-50 rounded-md bg-purple-600 px-4 py-2 font-semibold text-white shadow hover:bg-purple-700 focus:ring-2 focus:ring-purple-500 focus:outline-none disabled:opacity-50"
                :disabled="isQuerying"
            >
                <span v-if="isQuerying">Discovering...</span>
                <span v-else>Discover Peers</span>
            </button>
        </div>

        <div v-if="queryError" class="text-red-400">
            {{ queryError }}
        </div>

        <div v-if="lastQueryTime" class="text-sm text-gray-400">Last updated: {{ lastQueryTime.toLocaleString() }}</div>

        <div v-if="networkFilesCount > 0" class="text-sm text-gray-300">
            Found {{ networkFilesCount }} peer(s) sharing {{ allNetworkFilesCount }} file(s)
        </div>
        <div v-else-if="lastQueryTime" class="text-sm text-gray-300">No peers found on the index server.</div>
    </div>
</template>
