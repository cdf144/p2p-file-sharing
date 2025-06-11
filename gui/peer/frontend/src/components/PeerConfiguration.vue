<script lang="ts" setup>
import { reactive, ref, watch } from 'vue';

const DEBOUNCE_DELAY = 300; // ms

const props = defineProps<{
    peerConfig: {
        indexURL: string;
        shareDir: string;
        servePort: number;
        publicPort: number;
        tls: boolean;
        certFile: string;
        keyFile: string;
    };
    isServing: boolean;
    isLoading: boolean;
}>();

const emit = defineEmits([
    'update-config',
    'toggle-start-stop-peer',
    'select-directory',
    'select-cert-file',
    'select-key-file',
]);

const localConfig = reactive({ ...props.peerConfig });
// Delay for input changes to avoid excessive updates
const debounceTimer = ref<NodeJS.Timeout | null>(null);

watch(
    () => props.peerConfig,
    (newPeerConfig) => {
        localConfig.indexURL = newPeerConfig.indexURL;
        localConfig.shareDir = newPeerConfig.shareDir;
        localConfig.servePort = newPeerConfig.servePort;
        localConfig.publicPort = newPeerConfig.publicPort;
        localConfig.tls = newPeerConfig.tls;
        localConfig.certFile = newPeerConfig.certFile;
        localConfig.keyFile = newPeerConfig.keyFile;
    },
    { deep: true },
);

function handleInput() {
    if (debounceTimer.value) {
        clearTimeout(debounceTimer.value);
    }
    debounceTimer.value = setTimeout(() => {
        emit('update-config', { ...localConfig });
    }, DEBOUNCE_DELAY);
}

function handleTLSChange(event: Event) {
    const target = event.target as HTMLInputElement;
    localConfig.tls = target.checked;
    if (!localConfig.tls) {
        localConfig.certFile = '';
        localConfig.keyFile = '';
    }
    emit('update-config', { ...localConfig });
}

function selectDirectory() {
    emit('select-directory');
}

function selectCertFile() {
    emit('select-cert-file');
}

function selectKeyFile() {
    emit('select-key-file');
}

function toggleStartStopPeer() {
    emit('toggle-start-stop-peer', { ...localConfig });
}

const isTLSConfigured = (): boolean => {
    return localConfig.tls && localConfig.certFile !== '' && localConfig.keyFile !== '';
};
</script>

<template>
    <div class="space-y-4 rounded-lg bg-gray-700 p-6 shadow-lg">
        <h2 class="text-xl font-semibold">Peer Configuration</h2>

        <!-- Index Server URL -->
        <div>
            <label for="indexURL" class="block text-sm font-medium text-gray-300">Index Server URL:</label>
            <input
                id="indexURL"
                v-model="localConfig.indexURL"
                @input="handleInput"
                type="text"
                class="mt-1 block w-full rounded-md border-gray-500 bg-gray-600 px-3 py-2 shadow-sm focus:border-indigo-500 focus:ring-indigo-500 focus:outline-none sm:text-sm"
                :disabled="isLoading"
            />
        </div>

        <!-- Share Directory -->
        <div class="space-y-2">
            <label for="shareDir" class="block text-sm font-medium text-gray-300">Share Directory:</label>
            <div class="flex items-center space-x-2">
                <input
                    id="shareDir"
                    :value="props.peerConfig.shareDir"
                    type="text"
                    readonly
                    class="block h-10 w-full cursor-not-allowed rounded-md border-gray-500 bg-gray-500 px-3 py-2 shadow-sm sm:text-sm"
                    placeholder="Click 'Select Directory' button"
                />
                <button
                    @click="selectDirectory"
                    class="focus:ring-opacity-50 text-md h-10 rounded-md bg-blue-600 px-3 py-2 font-semibold whitespace-nowrap text-white shadow hover:bg-blue-700 focus:ring-2 focus:ring-blue-500 focus:outline-none disabled:opacity-50"
                    :disabled="isLoading"
                >
                    Select Directory
                </button>
            </div>
        </div>

        <!-- TLS Configuration Section -->
        <div class="space-y-4 rounded-lg bg-gray-600 p-4">
            <div class="flex items-center space-x-2">
                <input
                    id="enableTLS"
                    :checked="localConfig.tls"
                    @change="handleTLSChange"
                    type="checkbox"
                    class="h-4 w-4 rounded border-gray-500 bg-gray-700 text-indigo-600 focus:ring-indigo-500 focus:ring-offset-gray-800"
                    :disabled="isLoading"
                />
                <label for="enableTLS" class="text-sm font-medium text-gray-300">Enable TLS (Secure Connections)</label>
            </div>

            <div v-if="localConfig.tls" class="space-y-4">
                <div class="space-y-2">
                    <label for="certFile" class="block text-sm font-medium text-gray-300"
                        >Certificate File (.crt/.pem):</label
                    >
                    <div class="flex items-center space-x-2">
                        <input
                            id="certFile"
                            :value="localConfig.certFile"
                            type="text"
                            readonly
                            class="block h-10 w-full cursor-not-allowed rounded-md border-gray-500 bg-gray-500 px-3 py-2 shadow-sm sm:text-sm"
                            placeholder="Click 'Select Certificate' button"
                        />
                        <button
                            @click="selectCertFile"
                            class="focus:ring-opacity-50 text-md h-10 rounded-md bg-blue-600 px-3 py-2 font-semibold whitespace-nowrap text-white shadow hover:bg-blue-700 focus:ring-2 focus:ring-blue-500 focus:outline-none disabled:opacity-50"
                            :disabled="isLoading"
                        >
                            Select Certificate
                        </button>
                    </div>
                </div>

                <div class="space-y-2">
                    <label for="keyFile" class="block text-sm font-medium text-gray-300"
                        >Private Key File (.key/.pem):</label
                    >
                    <div class="flex items-center space-x-2">
                        <input
                            id="keyFile"
                            :value="localConfig.keyFile"
                            type="text"
                            readonly
                            class="block h-10 w-full cursor-not-allowed rounded-md border-gray-500 bg-gray-500 px-3 py-2 shadow-sm sm:text-sm"
                            placeholder="Click 'Select Private Key' button"
                        />
                        <button
                            @click="selectKeyFile"
                            class="focus:ring-opacity-50 text-md h-10 rounded-md bg-blue-600 px-3 py-2 font-semibold whitespace-nowrap text-white shadow hover:bg-blue-700 focus:ring-2 focus:ring-blue-500 focus:outline-none disabled:opacity-50"
                            :disabled="isLoading"
                        >
                            Select Private Key
                        </button>
                    </div>
                </div>

                <!-- TLS Status Indicator -->
                <div
                    v-if="localConfig.tls && (!localConfig.certFile || !localConfig.keyFile)"
                    class="bg-opacity-50 rounded-md bg-red-800 p-3"
                >
                    <div class="flex">
                        <div class="flex-shrink-0">
                            <svg class="h-5 w-5 text-red-300" viewBox="0 0 20 20" fill="currentColor">
                                <path
                                    fill-rule="evenodd"
                                    d="M10 18a8 8 0 100-16 8 8 0 000 16zM8.707 7.293a1 1 0 00-1.414 1.414L8.586 10l-1.293 1.293a1 1 0 101.414 1.414L10 11.414l1.293 1.293a1 1 0 001.414-1.414L11.414 10l1.293-1.293a1 1 0 00-1.414-1.414L10 8.586 8.707 7.293z"
                                    clip-rule="evenodd"
                                />
                            </svg>
                        </div>
                        <div class="ml-3">
                            <p class="text-sm text-red-300">
                                <strong>Warning:</strong> TLS is enabled but both certificate and private key files must
                                be selected for TLS to work properly.
                            </p>
                        </div>
                    </div>
                </div>

                <div v-else-if="isTLSConfigured()" class="bg-opacity-50 rounded-md bg-green-800 p-3">
                    <div class="flex">
                        <div class="flex-shrink-0">
                            <svg class="h-5 w-5 text-green-300" viewBox="0 0 20 20" fill="currentColor">
                                <path
                                    fill-rule="evenodd"
                                    d="M10 18a8 8 0 100-16 8 8 0 000 16zm3.707-9.293a1 1 0 00-1.414-1.414L9 10.586 7.707 9.293a1 1 0 00-1.414 1.414l2 2a1 1 0 001.414 0l4-4z"
                                    clip-rule="evenodd"
                                />
                            </svg>
                        </div>
                        <div class="ml-3">
                            <p class="text-sm text-green-300">
                                <strong>Ready:</strong> TLS is properly configured with both certificate and private key
                                files.
                            </p>
                        </div>
                    </div>
                </div>
            </div>
        </div>

        <!-- Ports -->
        <div class="grid grid-cols-1 gap-4 md:grid-cols-2">
            <div>
                <label for="servePort" class="block text-sm font-medium text-gray-300"
                    >Internal Serve Port (0 for random):</label
                >
                <input
                    id="servePort"
                    v-model.number="localConfig.servePort"
                    @input="handleInput"
                    type="number"
                    min="0"
                    class="mt-1 block w-full rounded-md border-gray-500 bg-gray-600 px-3 py-2 shadow-sm focus:border-indigo-500 focus:ring-indigo-500 focus:outline-none sm:text-sm"
                    :disabled="isLoading"
                />
            </div>
            <div>
                <label for="publicPort" class="block text-sm font-medium text-gray-300"
                    >Public Announce Port (0 for internal):</label
                >
                <input
                    id="publicPort"
                    v-model.number="localConfig.publicPort"
                    @input="handleInput"
                    type="number"
                    min="0"
                    class="mt-1 block w-full rounded-md border-gray-500 bg-gray-600 px-3 py-2 shadow-sm focus:border-indigo-500 focus:ring-indigo-500 focus:outline-none sm:text-sm"
                    :disabled="isLoading"
                />
            </div>
        </div>

        <!-- Start/Stop Button -->
        <button
            @click="toggleStartStopPeer"
            class="focus:ring-opacity-50 w-full rounded-md px-4 py-2 font-semibold shadow focus:ring-2 focus:outline-none disabled:opacity-50"
            :class="isServing ? 'bg-red-600 hover:bg-red-700' : 'bg-green-600 hover:bg-green-700'"
            :disabled="isLoading"
        >
            <span v-if="isLoading">Processing...</span>
            <span v-else-if="isServing">Stop Peer</span>
            <span v-else>Start Peer</span>
        </button>
    </div>
</template>
