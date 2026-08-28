export namespace backend {
	
	export class AgentDescriptorDTO {
	    name: string;
	    description: string;
	
	    static createFrom(source: any = {}) {
	        return new AgentDescriptorDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.description = source["description"];
	    }
	}
	export class BlackboardAttachmentResponse {
	    id: string;
	    original_name: string;
	    format: string;
	    size_bytes: number;
	    attached_at: string;
	
	    static createFrom(source: any = {}) {
	        return new BlackboardAttachmentResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.original_name = source["original_name"];
	        this.format = source["format"];
	        this.size_bytes = source["size_bytes"];
	        this.attached_at = source["attached_at"];
	    }
	}
	export class BlackboardFactResponse {
	    keywords: string[];
	    content: string;
	    author: string;
	
	    static createFrom(source: any = {}) {
	        return new BlackboardFactResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.keywords = source["keywords"];
	        this.content = source["content"];
	        this.author = source["author"];
	    }
	}
	export class BlackboardPlanStepResponse {
	    id: string;
	    summary: string;
	    description: string;
	    depends_on: string[];
	
	    static createFrom(source: any = {}) {
	        return new BlackboardPlanStepResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.summary = source["summary"];
	        this.description = source["description"];
	        this.depends_on = source["depends_on"];
	    }
	}
	export class BlackboardPlanResponse {
	    steps: BlackboardPlanStepResponse[];
	
	    static createFrom(source: any = {}) {
	        return new BlackboardPlanResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.steps = this.convertValues(source["steps"], BlackboardPlanStepResponse);
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
	
	export class BlackboardReflectionResponse {
	    summary: string;
	    hypotheses?: string[];
	    suggested_action?: string;
	    reasoning?: string;
	    failure_analysis?: string;
	    root_cause?: string;
	    action_plan?: string;
	    timestamp: string;
	
	    static createFrom(source: any = {}) {
	        return new BlackboardReflectionResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.summary = source["summary"];
	        this.hypotheses = source["hypotheses"];
	        this.suggested_action = source["suggested_action"];
	        this.reasoning = source["reasoning"];
	        this.failure_analysis = source["failure_analysis"];
	        this.root_cause = source["root_cause"];
	        this.action_plan = source["action_plan"];
	        this.timestamp = source["timestamp"];
	    }
	}
	export class BlackboardStepResponse {
	    step_id: string;
	    summary: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new BlackboardStepResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.step_id = source["step_id"];
	        this.summary = source["summary"];
	        this.error = source["error"];
	    }
	}
	export class BlackboardStateResponse {
	    task_id: string;
	    session_id: string;
	    status: string;
	    original_request: string;
	    plan?: BlackboardPlanResponse;
	    step_results: Record<string, BlackboardStepResponse>;
	    reflections: BlackboardReflectionResponse[];
	    facts: BlackboardFactResponse[];
	    attachments: BlackboardAttachmentResponse[];
	    final_output?: string;
	
	    static createFrom(source: any = {}) {
	        return new BlackboardStateResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.task_id = source["task_id"];
	        this.session_id = source["session_id"];
	        this.status = source["status"];
	        this.original_request = source["original_request"];
	        this.plan = this.convertValues(source["plan"], BlackboardPlanResponse);
	        this.step_results = this.convertValues(source["step_results"], BlackboardStepResponse, true);
	        this.reflections = this.convertValues(source["reflections"], BlackboardReflectionResponse);
	        this.facts = this.convertValues(source["facts"], BlackboardFactResponse);
	        this.attachments = this.convertValues(source["attachments"], BlackboardAttachmentResponse);
	        this.final_output = source["final_output"];
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
	
	export class ReasoningInfo {
	    options: string[];
	    default: string;
	
	    static createFrom(source: any = {}) {
	        return new ReasoningInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.options = source["options"];
	        this.default = source["default"];
	    }
	}
	export class ModelInfo {
	    name: string;
	    provider: string;
	    family: string;
	    vision: boolean;
	    reasoning?: ReasoningInfo;
	
	    static createFrom(source: any = {}) {
	        return new ModelInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.provider = source["provider"];
	        this.family = source["family"];
	        this.vision = source["vision"];
	        this.reasoning = this.convertValues(source["reasoning"], ReasoningInfo);
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
	export class ConfigProviderFull {
	    api_key: string;
	    base_url?: string;
	    models: string[];
	
	    static createFrom(source: any = {}) {
	        return new ConfigProviderFull(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.api_key = source["api_key"];
	        this.base_url = source["base_url"];
	        this.models = source["models"];
	    }
	}
	export class ConfigLLMResponse {
	    default_model: string;
	    anthropic: ConfigProviderFull;
	    openai_compatible: Record<string, ConfigProviderFull>;
	    anthropic_compatible: Record<string, ConfigProviderFull>;
	    chatgpt: ConfigProviderFull;
	    all_models: ModelInfo[];
	    models_ready: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ConfigLLMResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.default_model = source["default_model"];
	        this.anthropic = this.convertValues(source["anthropic"], ConfigProviderFull);
	        this.openai_compatible = this.convertValues(source["openai_compatible"], ConfigProviderFull, true);
	        this.anthropic_compatible = this.convertValues(source["anthropic_compatible"], ConfigProviderFull, true);
	        this.chatgpt = this.convertValues(source["chatgpt"], ConfigProviderFull);
	        this.all_models = this.convertValues(source["all_models"], ModelInfo);
	        this.models_ready = source["models_ready"];
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
	
	export class ExperimentalSettingsResponse {
	    enabled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ExperimentalSettingsResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	    }
	}
	export class ProxySettingsResponse {
	    enabled: boolean;
	    url: string;
	    bypass_list: string[];
	    tls_cert_dir: string;
	
	    static createFrom(source: any = {}) {
	        return new ProxySettingsResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.url = source["url"];
	        this.bypass_list = source["bypass_list"];
	        this.tls_cert_dir = source["tls_cert_dir"];
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
	    config_errors: string[];
	    llm: ConfigLLMResponse;
	    search: ConfigSearchResp;
	    proxy: ProxySettingsResponse;
	    experimental: ExperimentalSettingsResponse;
	
	    static createFrom(source: any = {}) {
	        return new ConfigResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.loaded = source["loaded"];
	        this.log_level = source["log_level"];
	        this.config_errors = source["config_errors"];
	        this.llm = this.convertValues(source["llm"], ConfigLLMResponse);
	        this.search = this.convertValues(source["search"], ConfigSearchResp);
	        this.proxy = this.convertValues(source["proxy"], ProxySettingsResponse);
	        this.experimental = this.convertValues(source["experimental"], ExperimentalSettingsResponse);
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
	
	
	export class FileIconResponse {
	    icon: string;
	    icon_color: string;
	
	    static createFrom(source: any = {}) {
	        return new FileIconResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.icon = source["icon"];
	        this.icon_color = source["icon_color"];
	    }
	}
	export class FrontendAPILifecycle {
	
	
	    static createFrom(source: any = {}) {
	        return new FrontendAPILifecycle(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	
	    }
	}
	export class GroupPolicyResponse {
	    policy: string;
	    blacklist: string[];
	
	    static createFrom(source: any = {}) {
	        return new GroupPolicyResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.policy = source["policy"];
	        this.blacklist = source["blacklist"];
	    }
	}
	export class ProviderConfigRequest {
	    api_key?: string;
	    base_url?: string;
	    models?: string[];
	
	    static createFrom(source: any = {}) {
	        return new ProviderConfigRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.api_key = source["api_key"];
	        this.base_url = source["base_url"];
	        this.models = source["models"];
	    }
	}
	export class LLMFullConfigRequest {
	    default_model: string;
	    anthropic?: ProviderConfigRequest;
	    openai_compatible?: Record<string, ProviderConfigRequest>;
	    anthropic_compatible?: Record<string, ProviderConfigRequest>;
	    chatgpt?: ProviderConfigRequest;
	
	    static createFrom(source: any = {}) {
	        return new LLMFullConfigRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.default_model = source["default_model"];
	        this.anthropic = this.convertValues(source["anthropic"], ProviderConfigRequest);
	        this.openai_compatible = this.convertValues(source["openai_compatible"], ProviderConfigRequest, true);
	        this.anthropic_compatible = this.convertValues(source["anthropic_compatible"], ProviderConfigRequest, true);
	        this.chatgpt = this.convertValues(source["chatgpt"], ProviderConfigRequest);
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
	export class ModelConfigRequest {
	    context_window: number;
	    output_limit: number;
	    tokenizer_type: string;
	    family: string;
	    protocol: string;
	    capabilities?: llm.ModelCapabilities;
	
	    static createFrom(source: any = {}) {
	        return new ModelConfigRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.context_window = source["context_window"];
	        this.output_limit = source["output_limit"];
	        this.tokenizer_type = source["tokenizer_type"];
	        this.family = source["family"];
	        this.protocol = source["protocol"];
	        this.capabilities = this.convertValues(source["capabilities"], llm.ModelCapabilities);
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
	export class ModelConfigResponse {
	    model: string;
	    context_window: number;
	    output_limit: number;
	    tokenizer_type: string;
	    family: string;
	    protocol: string;
	    capabilities: llm.ModelCapabilities;
	    default_context_window: number;
	    default_output_limit: number;
	    default_tokenizer_type: string;
	    default_family: string;
	    default_protocol: string;
	    default_capabilities: llm.ModelCapabilities;
	    has_override: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ModelConfigResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	        this.context_window = source["context_window"];
	        this.output_limit = source["output_limit"];
	        this.tokenizer_type = source["tokenizer_type"];
	        this.family = source["family"];
	        this.protocol = source["protocol"];
	        this.capabilities = this.convertValues(source["capabilities"], llm.ModelCapabilities);
	        this.default_context_window = source["default_context_window"];
	        this.default_output_limit = source["default_output_limit"];
	        this.default_tokenizer_type = source["default_tokenizer_type"];
	        this.default_family = source["default_family"];
	        this.default_protocol = source["default_protocol"];
	        this.default_capabilities = this.convertValues(source["default_capabilities"], llm.ModelCapabilities);
	        this.has_override = source["has_override"];
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
	
	export class OptimizePromptResponse {
	    optimized_prompt: string;
	    keywords: string[];
	    used_context: boolean;
	
	    static createFrom(source: any = {}) {
	        return new OptimizePromptResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.optimized_prompt = source["optimized_prompt"];
	        this.keywords = source["keywords"];
	        this.used_context = source["used_context"];
	    }
	}
	export class ProjectUIStateRequest {
	    project_id: string;
	    saved_session_id: string;
	    open_tabs: string[];
	    active_file: string;
	
	    static createFrom(source: any = {}) {
	        return new ProjectUIStateRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.project_id = source["project_id"];
	        this.saved_session_id = source["saved_session_id"];
	        this.open_tabs = source["open_tabs"];
	        this.active_file = source["active_file"];
	    }
	}
	export class ProjectUIStateResponse {
	    project_id: string;
	    saved_session_id: string;
	    open_tabs: string[];
	    active_file: string;
	    updated_at: string;
	
	    static createFrom(source: any = {}) {
	        return new ProjectUIStateResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.project_id = source["project_id"];
	        this.saved_session_id = source["saved_session_id"];
	        this.open_tabs = source["open_tabs"];
	        this.active_file = source["active_file"];
	        this.updated_at = source["updated_at"];
	    }
	}
	
	export class ProxySettingsRequest {
	    enabled: boolean;
	    url: string;
	    bypass_list: string[];
	    tls_cert_dir: string;
	
	    static createFrom(source: any = {}) {
	        return new ProxySettingsRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.url = source["url"];
	        this.bypass_list = source["bypass_list"];
	        this.tls_cert_dir = source["tls_cert_dir"];
	    }
	}
	
	
	export class ResearchGraph {
	    nodes: research.HypothesisNode[];
	    edges: research.HypothesisEdge[];
	
	    static createFrom(source: any = {}) {
	        return new ResearchGraph(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.nodes = this.convertValues(source["nodes"], research.HypothesisNode);
	        this.edges = this.convertValues(source["edges"], research.HypothesisEdge);
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
	export class ResearchMetrics {
	    total: number;
	    by_status: Record<string, number>;
	    confirmation_rate: number;
	    depth: number;
	    breadth: number;
	    active_front?: string[];
	
	    static createFrom(source: any = {}) {
	        return new ResearchMetrics(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.total = source["total"];
	        this.by_status = source["by_status"];
	        this.confirmation_rate = source["confirmation_rate"];
	        this.depth = source["depth"];
	        this.breadth = source["breadth"];
	        this.active_front = source["active_front"];
	    }
	}
	export class ResearchGraphDTO {
	    project_id: string;
	    graph: ResearchGraph;
	    metrics: ResearchMetrics;
	    has_report: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ResearchGraphDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.project_id = source["project_id"];
	        this.graph = this.convertValues(source["graph"], ResearchGraph);
	        this.metrics = this.convertValues(source["metrics"], ResearchMetrics);
	        this.has_report = source["has_report"];
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
	
	export class ResearchSeedResultDTO {
	    seeded: string[];
	    updated: string[];
	    current: string[];
	    preserved: string[];
	
	    static createFrom(source: any = {}) {
	        return new ResearchSeedResultDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.seeded = source["seeded"];
	        this.updated = source["updated"];
	        this.current = source["current"];
	        this.preserved = source["preserved"];
	    }
	}
	export class ResearchStatusDTO {
	    enabled: boolean;
	    project_id: string;
	    research_root: string;
	    root?: research.ResearchRoot;
	    seed_result?: ResearchSeedResultDTO;
	
	    static createFrom(source: any = {}) {
	        return new ResearchStatusDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.project_id = source["project_id"];
	        this.research_root = source["research_root"];
	        this.root = this.convertValues(source["root"], research.ResearchRoot);
	        this.seed_result = this.convertValues(source["seed_result"], ResearchSeedResultDTO);
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
	export class ReviewPromptMessage {
	    prompt_id: string;
	    content: string;
	
	    static createFrom(source: any = {}) {
	        return new ReviewPromptMessage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.prompt_id = source["prompt_id"];
	        this.content = source["content"];
	    }
	}
	export class SearchRequest {
	    query: string;
	    top_k: number;
	    file_pattern: string;
	    must_match: string[];
	    mode: string;
	
	    static createFrom(source: any = {}) {
	        return new SearchRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.query = source["query"];
	        this.top_k = source["top_k"];
	        this.file_pattern = source["file_pattern"];
	        this.must_match = source["must_match"];
	        this.mode = source["mode"];
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
	export class SecuritySettingsResponse {
	    groups: Record<string, GroupPolicyResponse>;
	    auto_approve_workspace_writes: boolean;
	    smart_approve: boolean;
	    judge_available: boolean;
	    execute_blacklist_defaults: string[];
	
	    static createFrom(source: any = {}) {
	        return new SecuritySettingsResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.groups = this.convertValues(source["groups"], GroupPolicyResponse, true);
	        this.auto_approve_workspace_writes = source["auto_approve_workspace_writes"];
	        this.smart_approve = source["smart_approve"];
	        this.judge_available = source["judge_available"];
	        this.execute_blacklist_defaults = source["execute_blacklist_defaults"];
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
	    model: string;
	    family: string;
	    fill_percent: number;
	    used_tokens?: number;
	    max_tokens?: number;
	
	    static createFrom(source: any = {}) {
	        return new SessionTokensResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.total_input_tokens = source["total_input_tokens"];
	        this.total_output_tokens = source["total_output_tokens"];
	        this.model = source["model"];
	        this.family = source["family"];
	        this.fill_percent = source["fill_percent"];
	        this.used_tokens = source["used_tokens"];
	        this.max_tokens = source["max_tokens"];
	    }
	}
	export class SkillDescriptorDTO {
	    name: string;
	    description: string;
	
	    static createFrom(source: any = {}) {
	        return new SkillDescriptorDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.description = source["description"];
	    }
	}
	export class SmallLLMCompactionResp {
	    keep_last: number;
	    block_size: number;
	    trigger_percent: number;
	
	    static createFrom(source: any = {}) {
	        return new SmallLLMCompactionResp(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.keep_last = source["keep_last"];
	        this.block_size = source["block_size"];
	        this.trigger_percent = source["trigger_percent"];
	    }
	}
	export class SmallLLMContextResp {
	    enabled: boolean;
	    compaction: SmallLLMCompactionResp;
	    tool_output_keep_last_n: number;
	    output_token_reserve: number;
	
	    static createFrom(source: any = {}) {
	        return new SmallLLMContextResp(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.compaction = this.convertValues(source["compaction"], SmallLLMCompactionResp);
	        this.tool_output_keep_last_n = source["tool_output_keep_last_n"];
	        this.output_token_reserve = source["output_token_reserve"];
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
	export class SmallLLMLoopHardeningResp {
	    enabled: boolean;
	    repeat_nudge_threshold: number;
	    parse_error_abort_threshold: number;
	    fruitless_nudge_threshold: number;
	    fruitless_abort_threshold: number;
	    same_tool_repeat_nudge_threshold: number;
	
	    static createFrom(source: any = {}) {
	        return new SmallLLMLoopHardeningResp(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.repeat_nudge_threshold = source["repeat_nudge_threshold"];
	        this.parse_error_abort_threshold = source["parse_error_abort_threshold"];
	        this.fruitless_nudge_threshold = source["fruitless_nudge_threshold"];
	        this.fruitless_abort_threshold = source["fruitless_abort_threshold"];
	        this.same_tool_repeat_nudge_threshold = source["same_tool_repeat_nudge_threshold"];
	    }
	}
	export class SmallLLMSamplingResp {
	    enabled: boolean;
	    temperature: number;
	    top_p: number;
	    top_k: number;
	    repetition_penalty: number;
	    reasoning_effort: string;
	
	    static createFrom(source: any = {}) {
	        return new SmallLLMSamplingResp(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.temperature = source["temperature"];
	        this.top_p = source["top_p"];
	        this.top_k = source["top_k"];
	        this.repetition_penalty = source["repetition_penalty"];
	        this.reasoning_effort = source["reasoning_effort"];
	    }
	}
	export class SmallLLMSystemPromptResp {
	    lite: boolean;
	    few_shot: boolean;
	    reasoning_scaffold: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SmallLLMSystemPromptResp(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.lite = source["lite"];
	        this.few_shot = source["few_shot"];
	        this.reasoning_scaffold = source["reasoning_scaffold"];
	    }
	}
	export class SmallLLMEssentialToolsResp {
	    enabled: boolean;
	    always_present: string[];
	    max_tools: number;
	    compact_descriptions: boolean;
	    protected_tools: string[];
	
	    static createFrom(source: any = {}) {
	        return new SmallLLMEssentialToolsResp(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.always_present = source["always_present"];
	        this.max_tools = source["max_tools"];
	        this.compact_descriptions = source["compact_descriptions"];
	        this.protected_tools = source["protected_tools"];
	    }
	}
	export class SmallLLMConfigResponse {
	    enabled: boolean;
	    essential_tools: SmallLLMEssentialToolsResp;
	    system_prompt: SmallLLMSystemPromptResp;
	    sampling: SmallLLMSamplingResp;
	    loop_hardening: SmallLLMLoopHardeningResp;
	    context: SmallLLMContextResp;
	
	    static createFrom(source: any = {}) {
	        return new SmallLLMConfigResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.essential_tools = this.convertValues(source["essential_tools"], SmallLLMEssentialToolsResp);
	        this.system_prompt = this.convertValues(source["system_prompt"], SmallLLMSystemPromptResp);
	        this.sampling = this.convertValues(source["sampling"], SmallLLMSamplingResp);
	        this.loop_hardening = this.convertValues(source["loop_hardening"], SmallLLMLoopHardeningResp);
	        this.context = this.convertValues(source["context"], SmallLLMContextResp);
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
	
	
	
	
	
	export class ToolInfo {
	    name: string;
	    description: string;
	    source: string;
	    group: string;
	    policy: string;
	
	    static createFrom(source: any = {}) {
	        return new ToolInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.description = source["description"];
	        this.source = source["source"];
	        this.group = source["group"];
	        this.policy = source["policy"];
	    }
	}
	export class UpdateInfo {
	    available: boolean;
	    current_version: string;
	    latest_version: string;
	    release_notes: string;
	    published_at: string;
	    html_url: string;
	    asset_name: string;
	
	    static createFrom(source: any = {}) {
	        return new UpdateInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.current_version = source["current_version"];
	        this.latest_version = source["latest_version"];
	        this.release_notes = source["release_notes"];
	        this.published_at = source["published_at"];
	        this.html_url = source["html_url"];
	        this.asset_name = source["asset_name"];
	    }
	}
	export class UpdateSettings {
	    auto_check: boolean;
	    skipped_version: string;
	    current_version: string;
	    operator_enabled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new UpdateSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.auto_check = source["auto_check"];
	        this.skipped_version = source["skipped_version"];
	        this.current_version = source["current_version"];
	        this.operator_enabled = source["operator_enabled"];
	    }
	}
	export class VectorIndexStatus {
	    state: string;
	    progress: number;
	    files_indexed: number;
	    total_files: number;
	    current_file: string;
	    branch: string;
	    phase: string;
	    indices: string[];
	
	    static createFrom(source: any = {}) {
	        return new VectorIndexStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.state = source["state"];
	        this.progress = source["progress"];
	        this.files_indexed = source["files_indexed"];
	        this.total_files = source["total_files"];
	        this.current_file = source["current_file"];
	        this.branch = source["branch"];
	        this.phase = source["phase"];
	        this.indices = source["indices"];
	    }
	}
	export class VectorStoreEntry {
	    file_path: string;
	    file_name: string;
	    content: string;
	    score: number;
	    start_line: number;
	    end_line: number;
	    language: string;
	    vector_score?: number;
	    lexical_score?: number;
	    vector_rank?: number;
	    lexical_rank?: number;
	
	    static createFrom(source: any = {}) {
	        return new VectorStoreEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.file_path = source["file_path"];
	        this.file_name = source["file_name"];
	        this.content = source["content"];
	        this.score = source["score"];
	        this.start_line = source["start_line"];
	        this.end_line = source["end_line"];
	        this.language = source["language"];
	        this.vector_score = source["vector_score"];
	        this.lexical_score = source["lexical_score"];
	        this.vector_rank = source["vector_rank"];
	        this.lexical_rank = source["lexical_rank"];
	    }
	}

}

export namespace desktop {
	
	export class PendingGoalProposal {
	    request_id: string;
	    condition: string;
	    verify: string;
	    verification_mode?: string;
	
	    static createFrom(source: any = {}) {
	        return new PendingGoalProposal(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.request_id = source["request_id"];
	        this.condition = source["condition"];
	        this.verify = source["verify"];
	        this.verification_mode = source["verification_mode"];
	    }
	}
	export class PendingAskUser {
	    request_id: string;
	    questions: tools.AskUserQuestion[];
	
	    static createFrom(source: any = {}) {
	        return new PendingAskUser(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.request_id = source["request_id"];
	        this.questions = this.convertValues(source["questions"], tools.AskUserQuestion);
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
	export class PendingPlanApproval {
	    request_id: string;
	    plan_path: string;
	    plan_content: string;
	
	    static createFrom(source: any = {}) {
	        return new PendingPlanApproval(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.request_id = source["request_id"];
	        this.plan_path = source["plan_path"];
	        this.plan_content = source["plan_content"];
	    }
	}
	export class PendingStepLimit {
	    request_id: string;
	    current_step: number;
	    max_steps: number;
	    reason?: string;
	
	    static createFrom(source: any = {}) {
	        return new PendingStepLimit(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.request_id = source["request_id"];
	        this.current_step = source["current_step"];
	        this.max_steps = source["max_steps"];
	        this.reason = source["reason"];
	    }
	}
	export class PendingToolConfirm {
	    confirm_id: string;
	    tool: string;
	    args: string;
	    reasoning?: string;
	    tool_call_id?: string;
	    disable_judge?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new PendingToolConfirm(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.confirm_id = source["confirm_id"];
	        this.tool = source["tool"];
	        this.args = source["args"];
	        this.reasoning = source["reasoning"];
	        this.tool_call_id = source["tool_call_id"];
	        this.disable_judge = source["disable_judge"];
	    }
	}
	export class PendingActionsResponse {
	    tool_confirms: PendingToolConfirm[];
	    step_limits: PendingStepLimit[];
	    plan_approvals: PendingPlanApproval[];
	    ask_user: PendingAskUser[];
	    goal_proposals: PendingGoalProposal[];
	
	    static createFrom(source: any = {}) {
	        return new PendingActionsResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tool_confirms = this.convertValues(source["tool_confirms"], PendingToolConfirm);
	        this.step_limits = this.convertValues(source["step_limits"], PendingStepLimit);
	        this.plan_approvals = this.convertValues(source["plan_approvals"], PendingPlanApproval);
	        this.ask_user = this.convertValues(source["ask_user"], PendingAskUser);
	        this.goal_proposals = this.convertValues(source["goal_proposals"], PendingGoalProposal);
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
	
	
	
	
	
	export class wailsLogAdapter {
	
	
	    static createFrom(source: any = {}) {
	        return new wailsLogAdapter(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	
	    }
	}

}

export namespace llm {
	
	export class ModelCapabilities {
	    attachment: boolean;
	    reasoning: boolean;
	    temperature: boolean;
	    tool_call: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ModelCapabilities(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.attachment = source["attachment"];
	        this.reasoning = source["reasoning"];
	        this.temperature = source["temperature"];
	        this.tool_call = source["tool_call"];
	    }
	}

}

export namespace mcp {
	
	export class ServerStatus {
	    name: string;
	    transport: string;
	    connected: boolean;
	    starting: boolean;
	    tool_count: number;
	    tools: string[];
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new ServerStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.transport = source["transport"];
	        this.connected = source["connected"];
	        this.starting = source["starting"];
	        this.tool_count = source["tool_count"];
	        this.tools = source["tools"];
	        this.error = source["error"];
	    }
	}

}

export namespace project {
	
	export class ProjectInfo {
	    id: string;
	    name: string;
	    workspace_path: string;
	    is_external: boolean;
	    is_no_project: boolean;
	    research_root: string;
	    is_research: boolean;
	    created_at: string;
	    last_active_at: string;
	
	    static createFrom(source: any = {}) {
	        return new ProjectInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.workspace_path = source["workspace_path"];
	        this.is_external = source["is_external"];
	        this.is_no_project = source["is_no_project"];
	        this.research_root = source["research_root"];
	        this.is_research = source["is_research"];
	        this.created_at = source["created_at"];
	        this.last_active_at = source["last_active_at"];
	    }
	}
	export class WorkDirectoryRecord {
	    id: string;
	    path: string;
	    description: string;
	    created_at: string;
	
	    static createFrom(source: any = {}) {
	        return new WorkDirectoryRecord(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.path = source["path"];
	        this.description = source["description"];
	        this.created_at = source["created_at"];
	    }
	}

}

export namespace research {
	
	export class Brief {
	    id: string;
	    title: string;
	    status?: string;
	    problem_domain?: string;
	    quarter?: string;
	    researchers?: string;
	    related_researches?: string;
	    research_question?: string;
	    success_criteria?: string;
	
	    static createFrom(source: any = {}) {
	        return new Brief(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.status = source["status"];
	        this.problem_domain = source["problem_domain"];
	        this.quarter = source["quarter"];
	        this.researchers = source["researchers"];
	        this.related_researches = source["related_researches"];
	        this.research_question = source["research_question"];
	        this.success_criteria = source["success_criteria"];
	    }
	}
	export class HypothesisEdge {
	    from: string;
	    to: string;
	
	    static createFrom(source: any = {}) {
	        return new HypothesisEdge(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.from = source["from"];
	        this.to = source["to"];
	    }
	}
	export class HypothesisNode {
	    id: string;
	    title: string;
	    status: string;
	    parents?: string[];
	    timebox?: string;
	    result?: string;
	
	    static createFrom(source: any = {}) {
	        return new HypothesisNode(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.status = source["status"];
	        this.parents = source["parents"];
	        this.timebox = source["timebox"];
	        this.result = source["result"];
	    }
	}
	export class HypothesisGraph {
	    nodes: HypothesisNode[];
	    edges: HypothesisEdge[];
	
	    static createFrom(source: any = {}) {
	        return new HypothesisGraph(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.nodes = this.convertValues(source["nodes"], HypothesisNode);
	        this.edges = this.convertValues(source["edges"], HypothesisEdge);
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
	
	export class IndexEntry {
	    id: string;
	    title?: string;
	    path?: string;
	
	    static createFrom(source: any = {}) {
	        return new IndexEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.path = source["path"];
	    }
	}
	export class Metrics {
	    total: number;
	    by_status: Record<string, number>;
	    confirmation_rate: number;
	    depth: number;
	    breadth: number;
	    active_front?: string[];
	
	    static createFrom(source: any = {}) {
	        return new Metrics(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.total = source["total"];
	        this.by_status = source["by_status"];
	        this.confirmation_rate = source["confirmation_rate"];
	        this.depth = source["depth"];
	        this.breadth = source["breadth"];
	        this.active_front = source["active_front"];
	    }
	}
	export class ResearchProject {
	    id: string;
	    brief: Brief;
	    graph: HypothesisGraph;
	    metrics: Metrics;
	    prior_art_count: number;
	    has_report: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ResearchProject(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.brief = this.convertValues(source["brief"], Brief);
	        this.graph = this.convertValues(source["graph"], HypothesisGraph);
	        this.metrics = this.convertValues(source["metrics"], Metrics);
	        this.prior_art_count = source["prior_art_count"];
	        this.has_report = source["has_report"];
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
	export class ResearchRoot {
	    path: string;
	    index: IndexEntry[];
	    projects: ResearchProject[];
	    active_project_id?: string;
	
	    static createFrom(source: any = {}) {
	        return new ResearchRoot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.index = this.convertValues(source["index"], IndexEntry);
	        this.projects = this.convertValues(source["projects"], ResearchProject);
	        this.active_project_id = source["active_project_id"];
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

export namespace review {
	
	export class FileComment {
	    id: string;
	    session_id: string;
	    file_path: string;
	    body: string;
	    created_at: string;
	
	    static createFrom(source: any = {}) {
	        return new FileComment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.session_id = source["session_id"];
	        this.file_path = source["file_path"];
	        this.body = source["body"];
	        this.created_at = source["created_at"];
	    }
	}
	export class HunkComment {
	    id: string;
	    session_id: string;
	    file_path: string;
	    hunk_id: string;
	    body: string;
	    created_at: string;
	
	    static createFrom(source: any = {}) {
	        return new HunkComment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.session_id = source["session_id"];
	        this.file_path = source["file_path"];
	        this.hunk_id = source["hunk_id"];
	        this.body = source["body"];
	        this.created_at = source["created_at"];
	    }
	}
	export class Review {
	    session_id: string;
	    status: string;
	    general_comment: string;
	    hunk_comments: HunkComment[];
	    file_comments: FileComment[];
	    updated_at: string;
	
	    static createFrom(source: any = {}) {
	        return new Review(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.session_id = source["session_id"];
	        this.status = source["status"];
	        this.general_comment = source["general_comment"];
	        this.hunk_comments = this.convertValues(source["hunk_comments"], HunkComment);
	        this.file_comments = this.convertValues(source["file_comments"], FileComment);
	        this.updated_at = source["updated_at"];
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
	
	export class AttachmentInfo {
	    id: string;
	    original_name: string;
	    format: string;
	    size_bytes: number;
	    is_image: boolean;
	    thumbnail?: string;
	    path?: string;
	    media_type?: string;
	
	    static createFrom(source: any = {}) {
	        return new AttachmentInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.original_name = source["original_name"];
	        this.format = source["format"];
	        this.size_bytes = source["size_bytes"];
	        this.is_image = source["is_image"];
	        this.thumbnail = source["thumbnail"];
	        this.path = source["path"];
	        this.media_type = source["media_type"];
	    }
	}
	export class ChatMessage {
	    id: number;
	    session_id: string;
	    role: string;
	    content: string;
	    reasoning_content?: string;
	    tool_calls?: number[];
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
	        this.reasoning_content = source["reasoning_content"];
	        this.tool_calls = source["tool_calls"];
	        this.metadata = source["metadata"];
	        this.created_at = source["created_at"];
	    }
	}
	export class Event {
	    session_id: string;
	    type: string;
	    data: any;
	
	    static createFrom(source: any = {}) {
	        return new Event(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.session_id = source["session_id"];
	        this.type = source["type"];
	        this.data = source["data"];
	    }
	}
	export class PasteResult {
	    kind: string;
	    text?: string;
	    files?: AttachmentInfo[];
	    rejected?: string;
	    skipped_images?: number;
	
	    static createFrom(source: any = {}) {
	        return new PasteResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.text = source["text"];
	        this.files = this.convertValues(source["files"], AttachmentInfo);
	        this.rejected = source["rejected"];
	        this.skipped_images = source["skipped_images"];
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
	export class SessionInfo {
	    id: string;
	    project_id: string;
	    name: string;
	    created_at: string;
	    last_active_at: string;
	    archived: boolean;
	    pinned: boolean;
	    active: boolean;
	    total_input_tokens: number;
	    total_output_tokens: number;
	    model: string;
	    family: string;
	    fill_percent: number;
	    has_unfinished_task: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SessionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.project_id = source["project_id"];
	        this.name = source["name"];
	        this.created_at = source["created_at"];
	        this.last_active_at = source["last_active_at"];
	        this.archived = source["archived"];
	        this.pinned = source["pinned"];
	        this.active = source["active"];
	        this.total_input_tokens = source["total_input_tokens"];
	        this.total_output_tokens = source["total_output_tokens"];
	        this.model = source["model"];
	        this.family = source["family"];
	        this.fill_percent = source["fill_percent"];
	        this.has_unfinished_task = source["has_unfinished_task"];
	    }
	}
	export class SessionRuntimeStatus {
	    active: boolean;
	    has_unfinished_task: boolean;
	    unfinished_task_id?: string;
	    paused: boolean;
	    compacting: boolean;
	    compaction_noop: boolean;
	    activity?: string;
	    streaming: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SessionRuntimeStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.active = source["active"];
	        this.has_unfinished_task = source["has_unfinished_task"];
	        this.unfinished_task_id = source["unfinished_task_id"];
	        this.paused = source["paused"];
	        this.compacting = source["compacting"];
	        this.compaction_noop = source["compaction_noop"];
	        this.activity = source["activity"];
	        this.streaming = source["streaming"];
	    }
	}
	export class TerminalCommand {
	    id: number;
	    session_id: string;
	    command: string;
	    created_at: string;
	
	    static createFrom(source: any = {}) {
	        return new TerminalCommand(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.session_id = source["session_id"];
	        this.command = source["command"];
	        this.created_at = source["created_at"];
	    }
	}

}

export namespace tools {
	
	export class AskUserOption {
	    label: string;
	    value: string;
	
	    static createFrom(source: any = {}) {
	        return new AskUserOption(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.label = source["label"];
	        this.value = source["value"];
	    }
	}
	export class AskUserQuestion {
	    id: string;
	    question: string;
	    options: AskUserOption[];
	    multi_select?: boolean;
	    recommended?: string[];
	
	    static createFrom(source: any = {}) {
	        return new AskUserQuestion(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.question = source["question"];
	        this.options = this.convertValues(source["options"], AskUserOption);
	        this.multi_select = source["multi_select"];
	        this.recommended = source["recommended"];
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

export namespace workspace {
	
	export class Branch {
	    name: string;
	    is_current: boolean;
	    kind: string;
	    upstream: string;
	
	    static createFrom(source: any = {}) {
	        return new Branch(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.is_current = source["is_current"];
	        this.kind = source["kind"];
	        this.upstream = source["upstream"];
	    }
	}
	export class BranchBase {
	    ref: string;
	    label: string;
	    type: string;
	    detail: string;
	
	    static createFrom(source: any = {}) {
	        return new BranchBase(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ref = source["ref"];
	        this.label = source["label"];
	        this.type = source["type"];
	        this.detail = source["detail"];
	    }
	}
	export class BranchInfo {
	    name: string;
	    upstream: string;
	    ahead: number;
	    behind: number;
	
	    static createFrom(source: any = {}) {
	        return new BranchInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.upstream = source["upstream"];
	        this.ahead = source["ahead"];
	        this.behind = source["behind"];
	    }
	}
	export class CommitFile {
	    status: string;
	    path: string;
	
	    static createFrom(source: any = {}) {
	        return new CommitFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.path = source["path"];
	    }
	}
	export class DiffStat {
	    added: number;
	    deleted: number;
	
	    static createFrom(source: any = {}) {
	        return new DiffStat(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.added = source["added"];
	        this.deleted = source["deleted"];
	    }
	}
	export class FileNode {
	    name: string;
	    path: string;
	    is_dir: boolean;
	    icon: string;
	    icon_color: string;
	    hidden: boolean;
	    gitignored: boolean;
	
	    static createFrom(source: any = {}) {
	        return new FileNode(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.is_dir = source["is_dir"];
	        this.icon = source["icon"];
	        this.icon_color = source["icon_color"];
	        this.hidden = source["hidden"];
	        this.gitignored = source["gitignored"];
	    }
	}
	export class GitHistoryCommit {
	    sha: string;
	    parents: string[];
	    author: string;
	    email: string;
	    date: string;
	    message: string;
	    refs: string[];
	
	    static createFrom(source: any = {}) {
	        return new GitHistoryCommit(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sha = source["sha"];
	        this.parents = source["parents"];
	        this.author = source["author"];
	        this.email = source["email"];
	        this.date = source["date"];
	        this.message = source["message"];
	        this.refs = source["refs"];
	    }
	}
	export class HunkDiffInfo {
	    old_start: number;
	    old_count: number;
	    new_start: number;
	    new_count: number;
	    old_change_start: number;
	    new_change_start: number;
	    staged: boolean;
	    diff: string;
	
	    static createFrom(source: any = {}) {
	        return new HunkDiffInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.old_start = source["old_start"];
	        this.old_count = source["old_count"];
	        this.new_start = source["new_start"];
	        this.new_count = source["new_count"];
	        this.old_change_start = source["old_change_start"];
	        this.new_change_start = source["new_change_start"];
	        this.staged = source["staged"];
	        this.diff = source["diff"];
	    }
	}
	export class MergeRebaseState {
	    is_merging: boolean;
	    is_rebasing: boolean;
	
	    static createFrom(source: any = {}) {
	        return new MergeRebaseState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.is_merging = source["is_merging"];
	        this.is_rebasing = source["is_rebasing"];
	    }
	}
	export class ReviewHunk {
	    raw: string;
	    old_start: number;
	    old_count: number;
	    new_start: number;
	    new_count: number;
	
	    static createFrom(source: any = {}) {
	        return new ReviewHunk(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.raw = source["raw"];
	        this.old_start = source["old_start"];
	        this.old_count = source["old_count"];
	        this.new_start = source["new_start"];
	        this.new_count = source["new_count"];
	    }
	}
	export class ReviewFileDiff {
	    path: string;
	    old_path?: string;
	    hunks: ReviewHunk[];
	
	    static createFrom(source: any = {}) {
	        return new ReviewFileDiff(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.old_path = source["old_path"];
	        this.hunks = this.convertValues(source["hunks"], ReviewHunk);
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
	
	export class StashEntry {
	    index: number;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new StashEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.index = source["index"];
	        this.message = source["message"];
	    }
	}

}

