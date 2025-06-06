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
	
	    static createFrom(source: any = {}) {
	        return new FileMeta(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.checksum = source["checksum"];
	        this.name = source["name"];
	        this.size = source["size"];
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

