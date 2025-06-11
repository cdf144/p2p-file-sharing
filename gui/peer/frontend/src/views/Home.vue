<script lang="ts" setup>
import { onMounted, onUnmounted, reactive } from 'vue';
import {
    DownloadFileWithDialog,
    FetchNetworkFiles,
    FetchPeersForFile,
    GetConnectedPeers,
    GetCurrentConfig,
    GetCurrentSharedFiles,
    SelectCertificateFile,
    SelectPrivateKeyFile,
    SelectShareDirectory,
    StartPeerLogic,
    StopPeerLogic,
    UpdatePeerConfig,
} from '../../wailsjs/go/main/App';
import { corepeer, main, protocol } from '../../wailsjs/go/models';
import { EventsOn, LogError } from '../../wailsjs/runtime/runtime';
import NetworkDiscovery from '../components/NetworkDiscovery.vue';
import NetworkFilesTable from '../components/NetworkFilesTable.vue';
import PeerConfiguration from '../components/PeerConfiguration.vue';
import PeerList from '../components/PeerList.vue';
import PeerStatus from '../components/PeerStatus.vue';
import SharedFilesTable from '../components/SharedFilesTable.vue';

const PEER_REGISTRY_REFRESH_INTERVAL = 1000; // 1s

const peerConfig = reactive({
    indexURL: 'http://localhost:9090',
    shareDir: '',
    servePort: 0,
    publicPort: 0,
    tls: false,
    certFile: '',
    keyFile: '',
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

const peerListState = reactive({
    peers: [] as corepeer.PeerRegistryInfo[],
    isLoading: false,
});

const downloadProgressMap = reactive<Record<string, main.DownloadProgressEvent>>({});

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

async function handleSelectCertFile() {
    try {
        peerState.statusMessage = 'Selecting certificate file...';
        const selectedFile = await SelectCertificateFile();
        peerConfig.certFile = selectedFile;
        updatePeerConfig({ ...peerConfig });

        if (selectedFile) {
            peerState.statusMessage = `Certificate file set to: ${selectedFile}. Configuration updated.`;
        } else {
            peerState.statusMessage = 'Certificate file selection cancelled. Configuration updated.';
        }
    } catch (error: any) {
        peerState.statusMessage = `Error selecting certificate file: ${error.message || error}`;
        LogError(`Error in handleSelectCertFile: ${error}`);
    }
}

async function handleSelectKeyFile() {
    try {
        peerState.statusMessage = 'Selecting private key file...';
        const selectedFile = await SelectPrivateKeyFile();
        peerConfig.keyFile = selectedFile;
        updatePeerConfig({ ...peerConfig });

        if (selectedFile) {
            peerState.statusMessage = `Private key file set to: ${selectedFile}. Configuration updated.`;
        } else {
            peerState.statusMessage = 'Private key file selection cancelled. Configuration updated.';
        }
    } catch (error: any) {
        peerState.statusMessage = `Error selecting private key file: ${error.message || error}`;
        LogError(`Error in handleSelectKeyFile: ${error}`);
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
            peerListState.peers = [];
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
        const effectiveTLS = configFromChild.tls && configFromChild.certFile !== '' && configFromChild.keyFile !== '';
        if (configFromChild.tls && !effectiveTLS) {
            throw new Error('TLS is enabled but certificate or private key file is missing');
        }
        peerConfig.indexURL = configFromChild.indexURL;
        peerConfig.servePort = configFromChild.servePort;
        peerConfig.publicPort = configFromChild.publicPort;
        peerConfig.tls = configFromChild.tls;
        peerConfig.certFile = configFromChild.certFile;
        peerConfig.keyFile = configFromChild.keyFile;

        await StartPeerLogic(
            peerConfig.indexURL,
            peerConfig.shareDir,
            Number(peerConfig.servePort),
            Number(peerConfig.publicPort),
            effectiveTLS,
            peerConfig.certFile,
            peerConfig.keyFile,
        );

        peerState.statusMessage = 'Peer started successfully.';
        peerState.isServing = true;

        await refreshPeerList();
    } catch (error: any) {
        LogError(`Error starting peer: ${error}`);
        peerState.statusMessage = `Error starting peer: ${error.message || error}`;
        peerState.isServing = false;
    } finally {
        peerState.isLoading = false;
    }
}

function updatePeerConfig(newConfig: typeof peerConfig) {
    const effectiveTLS = newConfig.tls && newConfig.certFile !== '' && newConfig.keyFile !== '';
    UpdatePeerConfig(
        newConfig.indexURL,
        newConfig.shareDir,
        Number(newConfig.servePort),
        Number(newConfig.publicPort),
        effectiveTLS,
        newConfig.certFile,
        newConfig.keyFile,
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

        if (peerState.isServing) {
            await refreshPeerList();
        }
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

        const peers = await FetchPeersForFile(file.checksum);
        if (!peers || peers.length === 0) {
            const errorMessage = `No peers found for file ${file.name} (checksum: ${file.checksum}).`;
            peerState.statusMessage = errorMessage;
            LogError(errorMessage);
            return;
        }

        await refreshPeerList();

        downloadProgressMap[file.checksum] = {
            fileChecksum: file.checksum,
            fileName: file.name,
            downloadedChunks: 0,
            totalChunks: file.numChunks,
            isComplete: false,
        };

        peerState.statusMessage = `Downloading ${file.name}...`;
        const result = await DownloadFileWithDialog(file.checksum, file.name);
        peerState.statusMessage = result;
    } catch (error: any) {
        LogError(`Error in download process for ${file.name}: ${error}`);
        peerState.statusMessage = `Error downloading file: ${error.message || error}`;
    }
}

let refreshPeerTimeoutId: NodeJS.Timeout | null = null;
let stopRefreshPeerLoop = false;

async function refreshPeerList() {
    if (peerListState.isLoading) {
        return;
    }
    peerListState.isLoading = true;
    try {
        const peers = await GetConnectedPeers();
        peerListState.peers = peers || [];
    } catch (error: any) {
        LogError(`Error fetching peer list: ${error}`);
        peerListState.peers = [];
    } finally {
        peerListState.isLoading = false;
    }
}

async function refreshPeerListLoop() {
    if (stopRefreshPeerLoop) {
        return;
    }
    await refreshPeerList();
    if (!stopRefreshPeerLoop) {
        refreshPeerTimeoutId = setTimeout(refreshPeerListLoop, PEER_REGISTRY_REFRESH_INTERVAL);
    }
}

let unsubscribeFilesScanned: (() => void) | undefined;
let unsubscribePeerConfigUpdated: (() => void) | undefined;
let unsubscribeDownloadProgress: (() => void) | undefined;

onMounted(async () => {
    try {
        const initialConfig = await GetCurrentConfig();
        peerConfig.indexURL = initialConfig.IndexURL;
        peerConfig.shareDir = initialConfig.ShareDir;
        peerConfig.servePort = initialConfig.ServePort;
        peerConfig.publicPort = initialConfig.PublicPort;
        peerConfig.tls = initialConfig.tls || false;
        peerConfig.certFile = initialConfig.certFile || '';
        peerConfig.keyFile = initialConfig.keyFile || '';

        const initialFiles = await GetCurrentSharedFiles();
        peerState.sharedFiles = initialFiles || [];

        await refreshPeerList();
    } catch (error: any) {
        LogError(`Error fetching initial state: ${error}`);
    }

    stopRefreshPeerLoop = false;
    refreshPeerListLoop();

    unsubscribeFilesScanned = EventsOn('filesScanned', (files: protocol.FileMeta[] | null) => {
        peerState.sharedFiles = files || [];
    });
    unsubscribePeerConfigUpdated = EventsOn('peerConfigUpdated', (config: corepeer.CorePeerConfig) => {
        peerConfig.indexURL = config.IndexURL;
        peerConfig.shareDir = config.ShareDir;
        peerConfig.servePort = config.ServePort;
        peerConfig.publicPort = config.PublicPort;
        peerConfig.tls = config.tls || false;
        peerConfig.certFile = config.certFile || '';
        peerConfig.keyFile = config.keyFile || '';
    });
    unsubscribeDownloadProgress = EventsOn('downloadProgress', (progress: main.DownloadProgressEvent) => {
        if (progress && progress.fileChecksum) {
            const existingProgress = downloadProgressMap[progress.fileChecksum];
            if (existingProgress) {
                downloadProgressMap[progress.fileChecksum] = {
                    ...existingProgress,
                    ...progress,
                };
            } else {
                downloadProgressMap[progress.fileChecksum] = progress;
            }

            if (progress.isComplete) {
                setTimeout(() => {
                    if (downloadProgressMap[progress.fileChecksum]?.isComplete) {
                        delete downloadProgressMap[progress.fileChecksum];
                    }
                }, 5000);
            }
        }
    });
});

onUnmounted(() => {
    if (unsubscribeFilesScanned) {
        unsubscribeFilesScanned();
    }
    if (unsubscribePeerConfigUpdated) {
        unsubscribePeerConfigUpdated();
    }
    if (unsubscribeDownloadProgress) {
        unsubscribeDownloadProgress();
    }
    stopRefreshPeerLoop = true;
    if (refreshPeerTimeoutId) {
        clearTimeout(refreshPeerTimeoutId);
    }
});
</script>

<template>
    <PeerConfiguration
        :peerConfig="peerConfig"
        :is-serving="peerState.isServing"
        :is-loading="peerState.isLoading"
        @update-config="updatePeerConfig"
        @select-directory="handleSelectDirectory"
        @select-cert-file="handleSelectCertFile"
        @select-key-file="handleSelectKeyFile"
        @toggle-start-stop-peer="handleToggleStartStopPeer"
    />

    <PeerStatus
        :status-message="peerState.statusMessage"
        :is-serving="peerState.isServing"
        :share-dir="peerConfig.shareDir"
    />

    <PeerList :peers="peerListState.peers" :is-loading="peerListState.isLoading" @refresh-peers="refreshPeerList" />

    <NetworkDiscovery
        :is-querying="networkState.isQuerying"
        :query-error="networkState.queryError"
        :last-query-time="networkState.lastQueryTime"
        :network-files="networkState.networkFiles"
        @discover-peers="queryNetwork"
    />

    <NetworkFilesTable
        :network-files="networkState.networkFiles"
        :download-progress="downloadProgressMap"
        @download-file="downloadFile"
    />

    <SharedFilesTable :shared-files="peerState.sharedFiles" :share-dir="peerConfig.shareDir" />
</template>
