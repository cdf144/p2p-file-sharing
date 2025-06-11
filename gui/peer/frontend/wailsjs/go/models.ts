export namespace corepeer {
	
	export class CorePeerConfig {
	    IndexURL: string;
	    ShareDir: string;
	    ServePort: number;
	    PublicPort: number;
	
	    static createFrom(source: any = {}) {
	        return new CorePeerConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.IndexURL = source["IndexURL"];
	        this.ShareDir = source["ShareDir"];
	        this.ServePort = source["ServePort"];
	        this.PublicPort = source["PublicPort"];
	    }
	}
	export class PeerRegistryInfo {
	    address: string;
	    status: number;
	    lastSeen: Date;
	    connectedAt: Date;
	    sharedFiles: protocol.FileMeta[];
	    failureCount: number;
	
	    static createFrom(source: any = {}) {
	        return new PeerRegistryInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.address = source["address"];
	        this.status = source["status"];
	        this.lastSeen = source["lastSeen"];
	        this.connectedAt = source["connectedAt"];
	        this.sharedFiles = this.convertValues(source["sharedFiles"], protocol.FileMeta);
	        this.failureCount = source["failureCount"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace main {
	
	export class DownloadProgressEvent {
	    fileChecksum: string;
	    fileName: string;
	    downloadedChunks: number;
	    totalChunks: number;
	    isComplete: boolean;
	    errorMessage?: string;
	
	    static createFrom(source: any = {}) {
	        return new DownloadProgressEvent(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fileChecksum = source["fileChecksum"];
	        this.fileName = source["fileName"];
	        this.downloadedChunks = source["downloadedChunks"];
	        this.totalChunks = source["totalChunks"];
	        this.isComplete = source["isComplete"];
	        this.errorMessage = source["errorMessage"];
	    }
	}

}

export namespace netip {
	
	export class AddrPort {
	
	
	    static createFrom(source: any = {}) {
	        return new AddrPort(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	
	    }
	}

}

export namespace protocol {
	
	export class FileMeta {
	    checksum: string;
	    name: string;
	    size: number;
	    chunkSize: number;
	    numChunks: number;
	    chunkHashes: string[];
	
	    static createFrom(source: any = {}) {
	        return new FileMeta(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.checksum = source["checksum"];
	        this.name = source["name"];
	        this.size = source["size"];
	        this.chunkSize = source["chunkSize"];
	        this.numChunks = source["numChunks"];
	        this.chunkHashes = source["chunkHashes"];
	    }
	}
	export class PeerInfo {
	    address: string;
	    files: FileMeta[];
	
	    static createFrom(source: any = {}) {
	        return new PeerInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.address = source["address"];
	        this.files = this.convertValues(source["files"], FileMeta);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

