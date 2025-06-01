<script lang="ts" setup>
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue';
import {
    DownloadFileWithDialog,
    QueryIndexServer,
    SelectShareDirectory,
    StartPeerLogic,
} from '../../wailsjs/go/main/App';
import { protocol } from '../../wailsjs/go/models';
import { EventsOn, LogError } from '../../wailsjs/runtime/runtime';
import { formatFileSize } from '../utils/formatFileSize';
import NetworkDiscovery from './NetworkDiscovery.vue';
import NetworkFilesTable from './NetworkFilesTable.vue';
import PeerConfiguration from './PeerConfiguration.vue';
import PeerStatus from './PeerStatus.vue';

const MAX_VISIBLE_PAGE_BUTTONS = 5;

const peerConfig = reactive({
    indexURL: 'http://localhost:9090',
    shareDir: '',
    servePort: 0,
    publicPort: 0,
});

const peerState = reactive({
    statusMessage: 'Peer is not running.',
    sharedFiles: [] as protocol.FileMeta[],
    isServing: false,
    isLoading: false,
});

const networkState = reactive({
    availablePeers: [] as protocol.PeerInfo[],
    isQuerying: false,
    queryError: '',
    lastQueryTime: null as Date | null,
});

const sharedFilesPerPage = ref(10);
const currentPage = ref(1);

const sharedFilesTotalPages = computed(() => {
    return Math.ceil(peerState.sharedFiles.length / sharedFilesPerPage.value);
});

const sharedFilesPaginated = computed(() => {
    const start = (currentPage.value - 1) * sharedFilesPerPage.value;
    const end = currentPage.value * sharedFilesPerPage.value;
    return peerState.sharedFiles.slice(start, end);
});

const allNetworkFiles = computed(() => {
    const files: Array<{ file: protocol.FileMeta; peer: protocol.PeerInfo }> = [];
    for (const peer of networkState.availablePeers) {
        for (const file of peer.Files) {
            files.push({ file, peer });
        }
    }
    return files;
});

const visiblePages = computed(() => {
    const total = sharedFilesTotalPages.value;
    const current = currentPage.value;
    if (total <= MAX_VISIBLE_PAGE_BUTTONS) {
        return Array.from({ length: total }, (_, i) => i + 1);
    }

    const half = Math.floor(MAX_VISIBLE_PAGE_BUTTONS / 2);
    let start = current - half;
    let end = current + half;
    if (start < 1) {
        start = 1;
        end = MAX_VISIBLE_PAGE_BUTTONS;
    }
    if (end > total) {
        end = total;
        start = total - MAX_VISIBLE_PAGE_BUTTONS + 1;
    }

    return Array.from({ length: end - start + 1 }, (_, i) => start + i);
});

const showLeftEllipsis = computed(() => {
    return visiblePages.value[0] > 1;
});
const showRightEllipsis = computed(() => {
    return visiblePages.value[visiblePages.value.length - 1] < sharedFilesTotalPages.value;
});

async function handleSelectDirectory() {
    try {
        peerConfig.shareDir = '';
        peerState.statusMessage = 'Selected directory. Scanning for files...';
        const selectedDir = await SelectShareDirectory();
        if (selectedDir) {
            peerConfig.shareDir = selectedDir;
            peerState.statusMessage = `Selected directory: ${selectedDir}. Scan results will appear below if files are found.`;
        }
    } catch (error) {
        peerState.statusMessage = `Error selecting directory: ${error}`;
    }
}

async function handleStartPeer(configFromChild: typeof peerConfig) {
    if (peerState.isServing) {
        peerState.statusMessage = 'Stop functionality not yet implemented. Please restart the app to stop.';
        return;
    }
    peerState.isLoading = true;
    peerState.statusMessage = 'Starting peer...';
    try {
        peerConfig.indexURL = configFromChild.indexURL;
        peerConfig.servePort = configFromChild.servePort;
        peerConfig.publicPort = configFromChild.publicPort;

        const result = await StartPeerLogic(
            peerConfig.indexURL,
            peerConfig.shareDir,
            Number(peerConfig.servePort),
            Number(peerConfig.publicPort),
        );
        peerState.statusMessage = result;
        peerState.isServing = true;
    } catch (error: any) {
        LogError(`Error starting peer: ${error}`);
        peerState.statusMessage = `Error starting peer: ${error.message || error}`;
        peerState.isServing = false;
    } finally {
        peerState.isLoading = false;
    }
}

function updatePeerConfig(newConfig: typeof peerConfig) {
    peerConfig.indexURL = newConfig.indexURL;
    peerConfig.servePort = newConfig.servePort;
    peerConfig.publicPort = newConfig.publicPort;
}

async function queryNetwork() {
    networkState.isQuerying = true;
    networkState.queryError = '';
    try {
        const peers = await QueryIndexServer(peerConfig.indexURL);
        networkState.availablePeers = peers;
        networkState.lastQueryTime = new Date();
    } catch (error: any) {
        networkState.queryError = `Error querying network: ${error.message || error}`;
        networkState.availablePeers = [];
    } finally {
        networkState.isQuerying = false;
    }
}

async function downloadFile(file: protocol.FileMeta, peer: protocol.PeerInfo) {
    try {
        const peerIP = peer.IP.toString();
        const result = await DownloadFileWithDialog(peerIP, peer.Port, file.Checksum, file.Name);
        peerState.statusMessage = result;
    } catch (error: any) {
        LogError(`Error downloading file: ${error}`);
        peerState.statusMessage = `Error downloading file: ${error.message || error}`;
    }
}

let unsubscribeFilesScanned: (() => void) | undefined;

onMounted(() => {
    unsubscribeFilesScanned = EventsOn('filesScanned', (files: protocol.FileMeta[] | null) => {
        peerState.sharedFiles = files || [];
        currentPage.value = 1;
    });
});

onUnmounted(() => {
    if (unsubscribeFilesScanned) {
        unsubscribeFilesScanned();
    }
});

function goToPage(page: number) {
    if (page >= 1 && page <= sharedFilesTotalPages.value) {
        currentPage.value = page;
    }
}

function prevPage() {
    if (currentPage.value > 1) {
        currentPage.value--;
    }
}

function nextPage() {
    if (currentPage.value < sharedFilesTotalPages.value) {
        currentPage.value++;
    }
}
</script>

<template>
    <main class="container mx-auto space-y-6 p-4">
        <h1 class="text-center text-2xl font-bold">P2P File Sharing Peer</h1>

        <PeerConfiguration
            :initial-config="peerConfig"
            :is-serving="peerState.isServing"
            :is-loading="peerState.isLoading"
            @update:config="updatePeerConfig"
            @select-directory="handleSelectDirectory"
            @start-peer="handleStartPeer"
        />

        <PeerStatus
            :status-message="peerState.statusMessage"
            :is-serving="peerState.isServing"
            :share-dir="peerConfig.shareDir"
        />

        <NetworkDiscovery
            :is-querying="networkState.isQuerying"
            :query-error="networkState.queryError"
            :last-query-time="networkState.lastQueryTime"
            :available-peers="networkState.availablePeers"
            :all-network-files-count="allNetworkFiles.length"
            @discover-peers="queryNetwork"
        />

        <NetworkFilesTable :all-network-files="allNetworkFiles" @download-file="downloadFile" />

        <!-- Shared Files from this Peer -->
        <div class="rounded-lg bg-gray-700 p-4 shadow" v-if="peerConfig.shareDir && peerState.sharedFiles.length > 0">
            <div class="mb-4 flex items-center justify-between">
                <h3 class="text-lg font-semibold">Shared Files (from: {{ peerConfig.shareDir }})</h3>

                <div class="flex items-center space-x-2">
                    <label for="filesPerPage" class="text-sm text-gray-300">Files per page:</label>
                    <select
                        id="filesPerPage"
                        v-model.number="sharedFilesPerPage"
                        @change="currentPage = 1"
                        class="appearance-none rounded border border-gray-500 bg-gray-600 px-2 py-1 text-sm text-white hover:border-indigo-500 focus:ring-1 focus:ring-indigo-500 focus:outline-none"
                    >
                        <option v-for="option in [5, 10, 20, 50]">
                            {{ option }}
                        </option>
                    </select>
                </div>
            </div>

            <div class="overflow-x-auto">
                <table class="min-w-full divide-y divide-gray-600">
                    <thead class="bg-gray-600">
                        <tr>
                            <th
                                scope="col"
                                class="px-6 py-3 text-left text-xs font-medium tracking-wider text-gray-300 uppercase"
                            >
                                Name
                            </th>
                            <th
                                scope="col"
                                class="px-6 py-3 text-left text-xs font-medium tracking-wider text-gray-300 uppercase"
                            >
                                Size
                            </th>
                            <th
                                scope="col"
                                class="px-6 py-3 text-left text-xs font-medium tracking-wider text-gray-300 uppercase"
                            >
                                Checksum (SHA256)
                            </th>
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-gray-600 bg-gray-700">
                        <tr v-for="file in sharedFilesPaginated" :key="file.Checksum">
                            <td class="px-6 py-4 text-left text-sm font-medium whitespace-nowrap text-gray-200">
                                {{ file.Name }}
                            </td>
                            <td class="px-6 py-4 text-left text-sm whitespace-nowrap text-gray-300">
                                {{ formatFileSize(file.Size) }}
                            </td>
                            <td class="truncate px-6 py-4 text-left text-sm text-gray-300" :title="file.Checksum">
                                {{ file.Checksum }}
                            </td>
                        </tr>
                    </tbody>
                </table>
            </div>

            <div class="mt-4 flex items-center justify-between" v-if="sharedFilesTotalPages > 1">
                <div class="text-sm text-gray-300">
                    Showing {{ (currentPage - 1) * sharedFilesPerPage + 1 }} to
                    {{ Math.min(currentPage * sharedFilesPerPage, peerState.sharedFiles.length) }}
                    of {{ peerState.sharedFiles.length }} files
                </div>

                <div class="flex items-center space-x-2">
                    <button
                        @click="prevPage"
                        :disabled="currentPage === 1"
                        class="rounded bg-gray-600 px-3 py-1 text-sm hover:bg-gray-500 disabled:cursor-not-allowed disabled:opacity-50"
                    >
                        Previous
                    </button>

                    <div class="flex space-x-1">
                        <button
                            v-if="showLeftEllipsis"
                            @click="goToPage(1)"
                            class="rounded bg-gray-600 px-3 py-1 text-sm hover:bg-gray-500"
                        >
                            1
                        </button>

                        <span v-if="showLeftEllipsis" class="px-2 text-gray-400">...</span>

                        <button
                            v-for="page in visiblePages"
                            :key="page"
                            @click="goToPage(page)"
                            :class="[
                                'rounded px-3 py-1 text-sm',
                                currentPage === page ? 'bg-blue-600 text-white' : 'bg-gray-600 hover:bg-gray-500',
                            ]"
                        >
                            {{ page }}
                        </button>

                        <span v-if="showRightEllipsis" class="px-2 text-gray-400">...</span>

                        <button
                            v-if="showRightEllipsis"
                            @click="goToPage(sharedFilesTotalPages)"
                            class="rounded bg-gray-600 px-3 py-1 text-sm hover:bg-gray-500"
                        >
                            {{ sharedFilesTotalPages }}
                        </button>
                    </div>

                    <button
                        @click="nextPage"
                        :disabled="currentPage === sharedFilesTotalPages"
                        class="rounded bg-gray-600 px-3 py-1 text-sm hover:bg-gray-500 disabled:cursor-not-allowed disabled:opacity-50"
                    >
                        Next
                    </button>
                </div>
            </div>
        </div>
        <div class="rounded-lg bg-gray-700 p-4 shadow" v-else-if="peerConfig.shareDir">
            <p class="text-red-400">No files found in '{{ peerConfig.shareDir }}' or directory not yet scanned.</p>
        </div>
    </main>
</template>
