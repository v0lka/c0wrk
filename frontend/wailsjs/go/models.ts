export namespace main {
	
	export class EpisodicSettingsRequest {
	    retention_days: number;
	    retrieval_limit: number;
	
	    static createFrom(source: any = {}) {
	        return new EpisodicSettingsRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.retention_days = source["retention_days"];
	        this.retrieval_limit = source["retrieval_limit"];
	    }
	}
	export class LLMSettingsRequest {
	    active_provider: string;
	    api_key: string;
	    base_url: string;
	    model: string;
	
	    static createFrom(source: any = {}) {
	        return new LLMSettingsRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.active_provider = source["active_provider"];
	        this.api_key = source["api_key"];
	        this.base_url = source["base_url"];
	        this.model = source["model"];
	    }
	}
	export class MemorySettingsRequest {
	    episodic: EpisodicSettingsRequest;
	
	    static createFrom(source: any = {}) {
	        return new MemorySettingsRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.episodic = this.convertValues(source["episodic"], EpisodicSettingsRequest);
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
	export class SearchSettingsRequest {
	    provider: string;
	    api_key: string;
	
	    static createFrom(source: any = {}) {
	        return new SearchSettingsRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider = source["provider"];
	        this.api_key = source["api_key"];
	    }
	}
	export class ToolPolicyResponse {
	    policy: string;
	    blacklist?: string[];
	
	    static createFrom(source: any = {}) {
	        return new ToolPolicyResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.policy = source["policy"];
	        this.blacklist = source["blacklist"];
	    }
	}
	export class SecuritySettingsResponse {
	    default_policy: string;
	    tool_policies: Record<string, ToolPolicyResponse>;
	
	    static createFrom(source: any = {}) {
	        return new SecuritySettingsResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.default_policy = source["default_policy"];
	        this.tool_policies = this.convertValues(source["tool_policies"], ToolPolicyResponse, true);
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

export namespace session {
	
	export class ChatMessage {
	    id: number;
	    session_id: string;
	    role: string;
	    content: string;
	    metadata: string;
	    created_at: string;
	
	    static createFrom(source: any = {}) {
	        return new ChatMessage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.session_id = source["session_id"];
	        this.role = source["role"];
	        this.content = source["content"];
	        this.metadata = source["metadata"];
	        this.created_at = source["created_at"];
	    }
	}
	export class SessionInfo {
	    id: string;
	    name: string;
	    created_at: string;
	    last_active_at: string;
	    archived: boolean;
	    active: boolean;
	    total_input_tokens: number;
	    total_output_tokens: number;
	
	    static createFrom(source: any = {}) {
	        return new SessionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.created_at = source["created_at"];
	        this.last_active_at = source["last_active_at"];
	        this.archived = source["archived"];
	        this.active = source["active"];
	        this.total_input_tokens = source["total_input_tokens"];
	        this.total_output_tokens = source["total_output_tokens"];
	    }
	}

}

