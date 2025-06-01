<script lang="ts" setup>
import { computed, ref, watch } from 'vue';
import { protocol } from '../../wailsjs/go/models';
import { formatFileSize } from '../utils/formatFileSize';

const MAX_VISIBLE_PAGE_BUTTONS = 5;

const props = defineProps<{
    sharedFiles: protocol.FileMeta[];
    shareDir: string;
}>();

const filesPerPage = ref(10);
const currentPage = ref(1);

const totalPages = computed(() => {
    return Math.ceil(props.sharedFiles.length / filesPerPage.value);
});

const paginatedFiles = computed(() => {
    const start = (currentPage.value - 1) * filesPerPage.value;
    const end = currentPage.value * filesPerPage.value;
    return props.sharedFiles.slice(start, end);
});

const visiblePages = computed(() => {
    const total = totalPages.value;
    const current = currentPage.value;
    if (total <= MAX_VISIBLE_PAGE_BUTTONS) {
        return Array.from({ length: total }, (_, i) => i + 1);
    }

    const half = Math.floor(MAX_VISIBLE_PAGE_BUTTONS / 2);
    let startPage = current - half;
    let endPage = current + half;

    if (startPage < 1) {
        startPage = 1;
        endPage = MAX_VISIBLE_PAGE_BUTTONS;
    }
    if (endPage > total) {
        endPage = total;
        startPage = total - MAX_VISIBLE_PAGE_BUTTONS + 1;
    }
    return Array.from({ length: endPage - startPage + 1 }, (_, i) => startPage + i);
});

const showLeftEllipsis = computed(() => {
    return visiblePages.value.length > 0 && visiblePages.value[0] > 1;
});

const showRightEllipsis = computed(() => {
    return visiblePages.value.length > 0 && visiblePages.value[visiblePages.value.length - 1] < totalPages.value;
});

function goToPage(page: number) {
    if (page >= 1 && page <= totalPages.value) {
        currentPage.value = page;
    }
}

function prevPage() {
    if (currentPage.value > 1) {
        currentPage.value--;
    }
}

function nextPage() {
    if (currentPage.value < totalPages.value) {
        currentPage.value++;
    }
}

// Reset to page 1 if the total number of files changes or filesPerPage changes
watch(
    () => props.sharedFiles.length,
    () => {
        if (currentPage.value > totalPages.value) {
            currentPage.value = Math.max(1, totalPages.value);
        } else if (props.sharedFiles.length > 0 && currentPage.value === 0) {
            currentPage.value = 1;
        }
    },
);

watch(filesPerPage, () => {
    currentPage.value = 1;
});
</script>

<template>
    <div class="rounded-lg bg-gray-700 p-4 shadow" v-if="props.shareDir && props.sharedFiles.length > 0">
        <!-- Header with Files Per Page Selector -->
        <div class="mb-4 flex items-center justify-between">
            <h3 class="text-lg font-semibold">Shared Files (from: {{ props.shareDir }})</h3>

            <div class="flex items-center space-x-2">
                <label for="sharedFilesPerPage" class="text-sm text-gray-300">Files per page:</label>
                <select
                    id="sharedFilesPerPage"
                    v-model.number="filesPerPage"
                    class="appearance-none rounded border border-gray-500 bg-gray-600 px-2 py-1 text-sm text-white hover:border-indigo-500 focus:ring-1 focus:ring-indigo-500 focus:outline-none"
                >
                    <option v-for="option in [5, 10, 20, 50]" :key="option">
                        {{ option }}
                    </option>
                </select>
            </div>
        </div>

        <!-- Files Table -->
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
                    <tr v-for="file in paginatedFiles" :key="file.Checksum">
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

        <!-- Pagination Controls -->
        <div class="mt-4 flex items-center justify-between" v-if="totalPages > 1">
            <div class="text-sm text-gray-300">
                Showing {{ (currentPage - 1) * filesPerPage + 1 }} to
                {{ Math.min(currentPage * filesPerPage, props.sharedFiles.length) }}
                of {{ props.sharedFiles.length }} files
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
                        @click="goToPage(totalPages)"
                        class="rounded bg-gray-600 px-3 py-1 text-sm hover:bg-gray-500"
                    >
                        {{ totalPages }}
                    </button>
                </div>

                <button
                    @click="nextPage"
                    :disabled="currentPage === totalPages"
                    class="rounded bg-gray-600 px-3 py-1 text-sm hover:bg-gray-500 disabled:cursor-not-allowed disabled:opacity-50"
                >
                    Next
                </button>
            </div>
        </div>
    </div>
    <div class="rounded-lg bg-gray-700 p-4 shadow" v-else-if="props.shareDir">
        <p class="text-red-400">No files found in '{{ props.shareDir }}' or directory not yet scanned.</p>
    </div>
</template>
