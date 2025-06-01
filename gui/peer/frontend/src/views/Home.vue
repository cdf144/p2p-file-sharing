<script lang="ts" setup>
import { computed, onMounted, onUnmounted, reactive } from 'vue';
import {
    DownloadFileWithDialog,
    QueryIndexServer,
    SelectShareDirectory,
    StartPeerLogic,
} from '../../wailsjs/go/main/App';
import { protocol } from '../../wailsjs/go/models';
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
    availablePeers: [] as protocol.PeerInfo[],
    isQuerying: false,
    queryError: '',
    lastQueryTime: null as Date | null,
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
    });
});
onUnmounted(() => {
    if (unsubscribeFilesScanned) {
        unsubscribeFilesScanned();
    }
});
</script>

<template>
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

    <SharedFilesTable :shared-files="peerState.sharedFiles" :share-dir="peerConfig.shareDir" />
</template>
