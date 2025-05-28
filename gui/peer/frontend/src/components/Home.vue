<script lang="ts" setup>
import { reactive, onMounted, onUnmounted } from 'vue';
import { StartPeerLogic, SelectShareDirectory } from '../../wailsjs/go/main/App';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import { protocol } from '../../wailsjs/go/models';

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

async function selectDirectory() {
    try {
        const selectedDir = await SelectShareDirectory();
        if (selectedDir) {
            peerConfig.shareDir = selectedDir;
            peerState.statusMessage = `Selected directory: ${selectedDir}. Scan results will appear below if files are found.`;
        }
    } catch (error) {
        console.error('Error selecting directory:', error);
        peerState.statusMessage = `Error selecting directory: ${error}`;
    }
}

async function startPeer() {
    if (peerState.isServing) {
        peerState.statusMessage =
            'Stop functionality not yet implemented. Please restart the app to stop.';
        return;
    }
    peerState.isLoading = true;
    peerState.statusMessage = 'Starting peer...';
    try {
        const result = await StartPeerLogic(
            peerConfig.indexURL,
            peerConfig.shareDir,
            Number(peerConfig.servePort),
            Number(peerConfig.publicPort),
        );
        peerState.statusMessage = result;
        peerState.isServing = true;
    } catch (error: any) {
        console.error('Error starting peer:', error);
        peerState.statusMessage = `Error starting peer: ${error.message || error}`;
        peerState.isServing = false;
    } finally {
        peerState.isLoading = false;
    }
}

let unsubscribeFilesScanned: (() => void) | undefined;

onMounted(() => {
    unsubscribeFilesScanned = EventsOn('filesScanned', (files: protocol.FileMeta[] | null) => {
        if (files) {
            peerState.sharedFiles = files;
            peerState.statusMessage = `Scanned ${files.length} files from ${
                peerConfig.shareDir || 'selected directory'
            }.`;
        } else {
            peerState.sharedFiles = [];
            peerState.statusMessage = `No files found or error scanning ${
                peerConfig.shareDir || 'selected directory'
            }.`;
        }
    });
});

onUnmounted(() => {
    if (unsubscribeFilesScanned) {
        unsubscribeFilesScanned();
    }
});

function formatFileSize(bytes: number): string {
    if (bytes === 0) return '0 Bytes';
    const k = 1024;
    const sizes = ['Bytes', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
}
</script>

<template>
    <main class="container mx-auto space-y-6 p-4">
        <h1 class="text-center text-2xl font-bold">P2P File Sharing Peer</h1>

        <div class="config-form space-y-4 rounded-lg bg-gray-700 p-6 shadow-lg">
            <h2 class="text-xl font-semibold">Peer Configuration</h2>
            <div>
                <label for="indexURL" class="block text-sm font-medium text-gray-300"
                    >Index Server URL:</label
                >
                <input
                    id="indexURL"
                    v-model="peerConfig.indexURL"
                    type="text"
                    class="mt-1 block w-full rounded-md border-gray-500 bg-gray-600 px-3 py-2 shadow-sm focus:border-indigo-500 focus:ring-indigo-500 focus:outline-none sm:text-sm"
                    :disabled="peerState.isServing || peerState.isLoading"
                />
            </div>

            <div class="flex items-end space-x-2">
                <div class="flex-grow">
                    <label for="shareDir" class="block text-sm font-medium text-gray-300"
                        >Share Directory:</label
                    >
                    <input
                        id="shareDir"
                        v-model="peerConfig.shareDir"
                        type="text"
                        readonly
                        class="mt-1 block w-full cursor-not-allowed rounded-md border-gray-500 bg-gray-500 px-3 py-2 shadow-sm sm:text-sm"
                        placeholder="Click 'Select Directory' button"
                    />
                </div>
                <button
                    @click="selectDirectory"
                    class="focus:ring-opacity-50 rounded-md bg-blue-600 px-4 py-2 font-semibold text-white shadow hover:bg-blue-700 focus:ring-2 focus:ring-blue-500 focus:outline-none disabled:opacity-50"
                    :disabled="peerState.isServing || peerState.isLoading"
                >
                    Select Directory
                </button>
            </div>

            <div class="grid grid-cols-1 gap-4 md:grid-cols-2">
                <div>
                    <label for="servePort" class="block text-sm font-medium text-gray-300"
                        >Internal Serve Port (0 for random):</label
                    >
                    <input
                        id="servePort"
                        v-model.number="peerConfig.servePort"
                        type="number"
                        min="0"
                        class="mt-1 block w-full rounded-md border-gray-500 bg-gray-600 px-3 py-2 shadow-sm focus:border-indigo-500 focus:ring-indigo-500 focus:outline-none sm:text-sm"
                        :disabled="peerState.isServing || peerState.isLoading"
                    />
                </div>
                <div>
                    <label for="publicPort" class="block text-sm font-medium text-gray-300"
                        >Public Announce Port (0 for internal):</label
                    >
                    <input
                        id="publicPort"
                        v-model.number="peerConfig.publicPort"
                        type="number"
                        min="0"
                        class="mt-1 block w-full rounded-md border-gray-500 bg-gray-600 px-3 py-2 shadow-sm focus:border-indigo-500 focus:ring-indigo-500 focus:outline-none sm:text-sm"
                        :disabled="peerState.isServing || peerState.isLoading"
                    />
                </div>
            </div>

            <button
                @click="startPeer"
                class="focus:ring-opacity-50 w-full rounded-md px-4 py-2 font-semibold shadow focus:ring-2 focus:outline-none disabled:opacity-50"
                :class="
                    peerState.isServing
                        ? 'bg-red-600 hover:bg-red-700'
                        : 'bg-green-600 hover:bg-green-700'
                "
                :disabled="peerState.isLoading"
            >
                <span v-if="peerState.isLoading">Processing...</span>
                <span v-else-if="peerState.isServing">Stop Peer (Not Implemented)</span>
                <span v-else>Start Peer</span>
            </button>
        </div>

        <div class="status-display rounded-lg bg-gray-700 p-4 shadow">
            <h3 class="font-semibold">Status:</h3>
            <p class="break-all text-gray-300">{{ peerState.statusMessage }}</p>
            <p v.if="peerState.isServing" class="font-semibold text-green-400">
                Peer is currently active.
            </p>
        </div>

        <div
            class="shared-files rounded-lg bg-gray-700 p-4 shadow"
            v-if="peerConfig.shareDir && peerState.sharedFiles.length > 0"
        >
            <h3 class="mb-2 text-lg font-semibold">
                Shared Files (from: {{ peerConfig.shareDir }})
            </h3>
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
                        <tr v-for="file in peerState.sharedFiles" :key="file.Checksum">
                            <td
                                class="px-6 py-4 text-sm font-medium whitespace-nowrap text-gray-200"
                            >
                                {{ file.Name }}
                            </td>
                            <td class="px-6 py-4 text-sm whitespace-nowrap text-gray-300">
                                {{ formatFileSize(file.Size) }}
                            </td>
                            <td
                                class="truncate px-6 py-4 text-sm whitespace-nowrap text-gray-300"
                                :title="file.Checksum"
                            >
                                {{ file.Checksum.substring(0, 32) }}...
                            </td>
                        </tr>
                    </tbody>
                </table>
            </div>
        </div>
        <div class="shared-files rounded-lg bg-gray-700 p-4 shadow" v-else-if="peerConfig.shareDir">
            <p class="text-gray-400">
                No files found in '{{ peerConfig.shareDir }}' or directory not yet scanned.
            </p>
        </div>
    </main>
</template>

<style scoped>
.truncate {
    max-width: 200px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
}
</style>
