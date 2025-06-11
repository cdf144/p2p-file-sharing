<script lang="ts" setup>
import { computed, onMounted, onUnmounted, reactive } from 'vue';
import {
    DownloadFileWithDialog,
    FetchNetworkFiles,
    FetchPeersForFile,
    GetCurrentConfig,
    GetCurrentSharedFiles,
    SelectShareDirectory,
    StartPeerLogic,
    StopPeerLogic,
    UpdatePeerConfig,
} from '../../wailsjs/go/main/App';
import { corepeer, protocol } from '../../wailsjs/go/models';
import { EventsOn, LogError } from '../../wailsjs/runtime/runtime';
import NetworkDiscovery from '../components/NetworkDiscovery.vue';
import NetworkFilesTable from '../components/NetworkFilesTable.vue';
import PeerConfiguration from '../components/PeerConfiguration.vue';
import PeerStatus from '../components/PeerStatus.vue';
import SharedFilesTable from '../components/SharedFilesTable.vue';

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
    networkFiles: [] as protocol.FileMeta[],
    isQuerying: false,
    queryError: '',
    lastQueryTime: null as Date | null,
});

const allNetworkFiles = computed(() => networkState.networkFiles || []);

async function handleSelectDirectory() {
    try {
        peerState.statusMessage = 'Selecting directory...';
        const selectedDir = await SelectShareDirectory();
        peerConfig.shareDir = selectedDir;
        updatePeerConfig({ ...peerConfig });

        if (selectedDir) {
            peerState.statusMessage = `Share directory set to: ${selectedDir}. Configuration updated. Scan results will appear if files are found.`;
        } else {
            peerState.statusMessage = 'Share directory cleared or selection cancelled. Configuration updated.';
        }
    } catch (error: any) {
        peerState.statusMessage = `Error selecting directory: ${error.message || error}`;
        LogError(`Error in handleSelectDirectory: ${error}`);
    }
}

async function handleToggleStartStopPeer(configFromChild: typeof peerConfig) {
    peerState.isLoading = true;

    // Stop
    if (peerState.isServing) {
        peerState.statusMessage = 'Stopping peer...';
        try {
            await StopPeerLogic();
            peerState.statusMessage = 'Peer stopped successfully.';
            peerState.isServing = false;
        } catch (error: any) {
            LogError(`Error stopping peer: ${error}`);
            peerState.statusMessage = `Error stopping peer: ${error.message || error}`;
        } finally {
            peerState.isLoading = false;
        }
        return;
    }

    // Start
    peerState.statusMessage = 'Starting peer...';
    try {
        peerConfig.indexURL = configFromChild.indexURL;
        peerConfig.servePort = configFromChild.servePort;
        peerConfig.publicPort = configFromChild.publicPort;

        await StartPeerLogic(
            peerConfig.indexURL,
            peerConfig.shareDir,
            Number(peerConfig.servePort),
            Number(peerConfig.publicPort),
        );

        peerState.statusMessage = 'Peer started successfully.';
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
    UpdatePeerConfig(
        newConfig.indexURL,
        newConfig.shareDir,
        Number(newConfig.servePort),
        Number(newConfig.publicPort),
    ).catch((error: any) => {
        LogError(`Error updating peer config: ${error}`);
    });
}

async function queryNetwork() {
    networkState.isQuerying = true;
    networkState.queryError = '';
    try {
        const files = await FetchNetworkFiles();
        networkState.networkFiles = files ? files : [];
        networkState.lastQueryTime = new Date();
    } catch (error: any) {
        networkState.queryError = `Error querying network: ${error.message || error}`;
        networkState.networkFiles = [];
    } finally {
        networkState.isQuerying = false;
    }
}

async function downloadFile(file: protocol.FileMeta) {
    try {
        peerState.statusMessage = `Fetching peers for ${file.name}...`;
        // Fetch peers that have this file using its checksum
        // The indexURL is implicitly used by the backend's corePeer configuration
        const peers = await FetchPeersForFile(file.checksum);

        if (!peers || peers.length === 0) {
            const errorMessage = `No peers found for file ${file.name} (checksum: ${file.checksum}).`;
            peerState.statusMessage = errorMessage;
            LogError(errorMessage);
            return;
        }

        peerState.statusMessage = `Downloading ${file.name} from peer...`;
        const result = await DownloadFileWithDialog(file.checksum, file.name);
        peerState.statusMessage = result;
    } catch (error: any) {
        LogError(`Error in download process for ${file.name}: ${error}`);
        peerState.statusMessage = `Error downloading file: ${error.message || error}`;
    }
}

let unsubscribeFilesScanned: (() => void) | undefined;
let unsubscribePeerConfigUpdated: (() => void) | undefined;

onMounted(async () => {
    try {
        const initialConfig = await GetCurrentConfig();
        peerConfig.indexURL = initialConfig.IndexURL;
        peerConfig.shareDir = initialConfig.ShareDir;
        peerConfig.servePort = initialConfig.ServePort;
        peerConfig.publicPort = initialConfig.PublicPort;

        const initialFiles = await GetCurrentSharedFiles();
        peerState.sharedFiles = initialFiles || [];
    } catch (error: any) {
        LogError(`Error fetching initial state: ${error}`);
    }

    unsubscribeFilesScanned = EventsOn('filesScanned', (files: protocol.FileMeta[] | null) => {
        peerState.sharedFiles = files || [];
    });
    unsubscribePeerConfigUpdated = EventsOn('peerConfigUpdated', (config: corepeer.CorePeerConfig) => {
        peerConfig.indexURL = config.IndexURL;
        peerConfig.shareDir = config.ShareDir;
        peerConfig.servePort = config.ServePort;
        peerConfig.publicPort = config.PublicPort;
    });
});

onUnmounted(() => {
    if (unsubscribeFilesScanned) {
        unsubscribeFilesScanned();
    }
    if (unsubscribePeerConfigUpdated) {
        unsubscribePeerConfigUpdated();
    }
});
</script>

<template>
    <PeerConfiguration
        :peerConfig="peerConfig"
        :is-serving="peerState.isServing"
        :is-loading="peerState.isLoading"
        @update:config="updatePeerConfig"
        @select-directory="handleSelectDirectory"
        @toggle-start-stop-peer="handleToggleStartStopPeer"
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
        :network-files="networkState.networkFiles"
        :all-network-files-count="allNetworkFiles.length"
        @discover-peers="queryNetwork"
    />

    <NetworkFilesTable :all-network-files="allNetworkFiles" @download-file="downloadFile" />

    <SharedFilesTable :shared-files="peerState.sharedFiles" :share-dir="peerConfig.shareDir" />
</template>
