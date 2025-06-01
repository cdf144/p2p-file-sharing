<script lang="ts" setup>
import { reactive } from 'vue';

const props = defineProps<{
    initialConfig: {
        indexURL: string;
        shareDir: string;
        servePort: number;
        publicPort: number;
    };
    isServing: boolean;
    isLoading: boolean;
}>();

const emit = defineEmits(['update:config', 'start-peer', 'select-directory']);

const localConfig = reactive({ ...props.initialConfig });

function handleInput() {
    emit('update:config', { ...localConfig });
}

function selectDirectory() {
    emit('select-directory');
}

function startPeer() {
    emit('start-peer', { ...localConfig });
}
</script>

<template>
    <div class="space-y-4 rounded-lg bg-gray-700 p-6 shadow-lg">
        <h2 class="text-xl font-semibold">Peer Configuration</h2>
        <div>
            <label for="indexURL" class="block text-sm font-medium text-gray-300">Index Server URL:</label>
            <input
                id="indexURL"
                v-model="localConfig.indexURL"
                @input="handleInput"
                type="text"
                class="mt-1 block w-full rounded-md border-gray-500 bg-gray-600 px-3 py-2 shadow-sm focus:border-indigo-500 focus:ring-indigo-500 focus:outline-none sm:text-sm"
                :disabled="isServing || isLoading"
            />
        </div>

        <div class="flex items-end space-x-2">
            <div class="flex-grow">
                <label for="shareDir" class="block text-sm font-medium text-gray-300">Share Directory:</label>
                <input
                    id="shareDir"
                    :value="initialConfig.shareDir"
                    type="text"
                    readonly
                    class="mt-1 block w-full cursor-not-allowed rounded-md border-gray-500 bg-gray-500 px-3 py-2 shadow-sm sm:text-sm"
                    placeholder="Click 'Select Directory' button"
                />
            </div>
            <button
                @click="selectDirectory"
                class="focus:ring-opacity-50 rounded-md bg-blue-600 px-4 py-2 font-semibold text-white shadow hover:bg-blue-700 focus:ring-2 focus:ring-blue-500 focus:outline-none disabled:opacity-50"
                :disabled="isServing || isLoading"
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
                    v-model.number="localConfig.servePort"
                    @input="handleInput"
                    type="number"
                    min="0"
                    class="mt-1 block w-full rounded-md border-gray-500 bg-gray-600 px-3 py-2 shadow-sm focus:border-indigo-500 focus:ring-indigo-500 focus:outline-none sm:text-sm"
                    :disabled="isServing || isLoading"
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
                    :disabled="isServing || isLoading"
                />
            </div>
        </div>

        <button
            @click="startPeer"
            class="focus:ring-opacity-50 w-full rounded-md px-4 py-2 font-semibold shadow focus:ring-2 focus:outline-none disabled:opacity-50"
            :class="isServing ? 'bg-red-600 hover:bg-red-700' : 'bg-green-600 hover:bg-green-700'"
            :disabled="isLoading"
        >
            <span v-if="isLoading">Processing...</span>
            <span v-else-if="isServing">Stop Peer (Not Implemented)</span>
            <span v-else>Start Peer</span>
        </button>
    </div>
</template>
