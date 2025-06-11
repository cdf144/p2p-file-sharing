<script lang="ts" setup>
import { corepeer } from '../../wailsjs/go/models';
import { formatTime } from '../utils/formatTime';

const props = defineProps<{
    peers: corepeer.PeerRegistryInfo[];
    isLoading?: boolean;
}>();

const emit = defineEmits(['refresh-peers']);

const getStatusText = (status: number): string => {
    const statusMap: { [key: number]: string } = {
        0: 'Connecting',
        1: 'Connected',
        2: 'Disconnected',
        3: 'Unreachable',
    };
    return statusMap[status] || 'Unknown';
};

const getStatusClass = (status: number): string => {
    const classMap: { [key: number]: string } = {
        0: 'bg-yellow-600 text-yellow-100',
        1: 'bg-green-600 text-green-100',
        2: 'bg-gray-600 text-gray-100',
        3: 'bg-red-600 text-red-100',
    };
    return classMap[status] || 'bg-gray-600 text-gray-100';
};

const handleRefresh = () => {
    emit('refresh-peers');
};
</script>

<template>
    <div class="rounded-lg bg-gray-700 p-4 shadow">
        <div class="mb-4 flex items-center justify-between">
            <h3 class="text-lg font-semibold">Connected Peers ({{ props.peers.length }})</h3>
            <button
                @click="handleRefresh"
                :disabled="isLoading"
                class="rounded bg-blue-600 px-3 py-1 text-sm transition-colors duration-200 hover:bg-blue-700 disabled:bg-blue-500 disabled:opacity-50"
            >
                Refresh
            </button>
        </div>

        <div v-if="props.peers.length > 0" class="overflow-x-auto">
            <table class="min-w-full divide-y divide-gray-600">
                <thead class="bg-gray-600">
                    <tr>
                        <th class="px-6 py-3 text-xs font-medium tracking-wider text-gray-300 uppercase">Address</th>
                        <th class="px-6 py-3 text-xs font-medium tracking-wider text-gray-300 uppercase">Status</th>
                        <th class="px-6 py-3 text-xs font-medium tracking-wider text-gray-300 uppercase">Files</th>
                        <th class="px-6 py-3 text-xs font-medium tracking-wider text-gray-300 uppercase">Failures</th>
                        <th class="px-6 py-3 text-xs font-medium tracking-wider text-gray-300 uppercase">Last Seen</th>
                    </tr>
                </thead>
                <tbody class="divide-y divide-gray-600">
                    <tr v-for="peer in props.peers" :key="peer.address" class="hover:bg-gray-600">
                        <td class="px-6 py-4 text-sm text-gray-300">
                            {{ peer.address }}
                        </td>
                        <td class="px-6 py-4 text-sm">
                            <span :class="getStatusClass(peer.status)" class="rounded-full px-2 py-1 text-xs">
                                {{ getStatusText(peer.status) }}
                            </span>
                        </td>
                        <td class="px-6 py-4 text-sm text-gray-300">
                            {{ peer.sharedFiles.length }}
                        </td>
                        <td class="px-6 py-4 text-sm text-gray-300">
                            <span v-if="peer.failureCount > 0" class="text-red-400"> {{ peer.failureCount }} </span>
                            <span v-else class="text-green-400">0</span>
                        </td>
                        <td class="px-6 py-4 text-sm text-gray-300">
                            {{ formatTime(peer.lastSeen) }}
                        </td>
                    </tr>
                </tbody>
            </table>
        </div>

        <div v-else class="py-8 text-center">
            <p class="text-gray-400">No peers connected.</p>
        </div>
    </div>
</template>
