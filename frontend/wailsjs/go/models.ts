export namespace main {
	
	export class ConfigProviderFull {
	    base_url: string;
	    api_key: string;
	    model: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigProviderFull(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.base_url = source["base_url"];
	        this.api_key = source["api_key"];
	        this.model = source["model"];
	    }
	}
	export class ConfigProviderKeyModel {
	    api_key: string;
	    model: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigProviderKeyModel(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.api_key = source["api_key"];
	        this.model = source["model"];
	    }
	}
	export class ConfigLLMResponse {
	    active_provider: string;
	    anthropic: ConfigProviderKeyModel;
	    gemini: ConfigProviderKeyModel;
	    lmstudio: ConfigProviderFull;
	    openai_compatible: ConfigProviderFull;
	    chatgpt: ConfigProviderKeyModel;
	
	    static createFrom(source: any = {}) {
	        return new ConfigLLMResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.active_provider = source["active_provider"];
	        this.anthropic = this.convertValues(source["anthropic"], ConfigProviderKeyModel);
	        this.gemini = this.convertValues(source["gemini"], ConfigProviderKeyModel);
	        this.lmstudio = this.convertValues(source["lmstudio"], ConfigProviderFull);
	        this.openai_compatible = this.convertValues(source["openai_compatible"], ConfigProviderFull);
	        this.chatgpt = this.convertValues(source["chatgpt"], ConfigProviderKeyModel);
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
	export class ConfigMemResponse {
	    database: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigMemResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.database = source["database"];
	    }
	}
	
	
	export class ConfigSearchResp {
	    provider: string;
	    api_key: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigSearchResp(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider = source["provider"];
	        this.api_key = source["api_key"];
	    }
	}
	export class ConfigResponse {
	    loaded: boolean;
	    log_level: string;
	    theme: string;
	    config_migrated: boolean;
	    config_migration_msg: string;
	    config_errors: string[];
	    llm: ConfigLLMResponse;
	    memory: ConfigMemResponse;
	    search: ConfigSearchResp;
	
	    static createFrom(source: any = {}) {
	        return new ConfigResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.loaded = source["loaded"];
	        this.log_level = source["log_level"];
	        this.theme = source["theme"];
	        this.config_migrated = source["config_migrated"];
	        this.config_migration_msg = source["config_migration_msg"];
	        this.config_errors = source["config_errors"];
	        this.llm = this.convertValues(source["llm"], ConfigLLMResponse);
	        this.memory = this.convertValues(source["memory"], ConfigMemResponse);
	        this.search = this.convertValues(source["search"], ConfigSearchResp);
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
	
	export class FileNode {
	    name: string;
	    path: string;
	    is_dir: boolean;
	
	    static createFrom(source: any = {}) {
	        return new FileNode(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.is_dir = source["is_dir"];
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
	export class SessionTokensResponse {
	    total_input_tokens: number;
	    total_output_tokens: number;
	
	    static createFrom(source: any = {}) {
	        return new SessionTokensResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.total_input_tokens = source["total_input_tokens"];
	        this.total_output_tokens = source["total_output_tokens"];
	    }
	}

}

export namespace session {
	
	export class ChatMessage {
	    id: number;
	    session_id: string;
	    role: string;
	    content: string;
	    metadata: number[];
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

