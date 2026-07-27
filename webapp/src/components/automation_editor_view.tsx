import {createAutomation, getAgentTools, getAIBots, getAutomation, updateAutomation} from 'client';
import React, {useCallback, useEffect, useState} from 'react';
import {useHistory, useParams, useRouteMatch} from 'react-router-dom';
import type {TriggerFormState} from 'triggers';
import {getAllTriggerConfigs, getTriggerConfig} from 'triggers';

import 'triggers/channel_created';
import 'triggers/membership_changed';
import 'triggers/message_posted';
import 'triggers/schedule';
import 'triggers/user_joined_team';
import type {AIBotInfo, AIToolInfo, Action} from 'types';
import {getTriggerType} from 'types';

type RequestAs = '' | 'triggerer' | 'creator';

interface ActionForm {
    id: string;
    type: string;
    channel_id: string;
    reply_to_post_id: string;
    as_bot_id: string;
    body: string;
    system_prompt: string;
    prompt: string;
    provider_id: string;
    allowed_tool_refs: string[];
    request_as: RequestAs;

    /** Parsed guardrail channel IDs (from guardrails.channel_ids). */
    guardrail_channel_ids: string[];
}

const styles = {
    formGroup: {
        marginBottom: 16,
    } as React.CSSProperties,
    label: {
        display: 'block',
        fontWeight: 600,
        marginBottom: 4,
    } as React.CSSProperties,
    input: {
        width: '100%',
        padding: '6px 8px',
        border: '1px solid rgba(var(--center-channel-color-rgb), 0.2)',
        borderRadius: 4,
        fontSize: 14,
        boxSizing: 'border-box' as const,
        color: 'var(--center-channel-color)',
        backgroundColor: 'var(--center-channel-bg)',
    },
    textarea: {
        width: '100%',
        padding: '6px 8px',
        border: '1px solid rgba(var(--center-channel-color-rgb), 0.2)',
        borderRadius: 4,
        fontSize: 14,
        minHeight: 80,
        boxSizing: 'border-box' as const,
        color: 'var(--center-channel-color)',
        backgroundColor: 'var(--center-channel-bg)',
    },
    select: {
        width: '100%',
        padding: '6px 8px',
        border: '1px solid rgba(var(--center-channel-color-rgb), 0.2)',
        borderRadius: 4,
        fontSize: 14,
        color: 'var(--center-channel-color)',
        backgroundColor: 'var(--center-channel-bg)',
    } as React.CSSProperties,
    btnPrimary: {
        padding: '8px 16px',
        backgroundColor: 'var(--button-bg)',
        color: 'var(--button-color)',
        border: 'none',
        borderRadius: 4,
        cursor: 'pointer',
        fontSize: 14,
        marginRight: 8,
    } as React.CSSProperties,
    btnSecondary: {
        padding: '8px 16px',
        backgroundColor: 'var(--center-channel-bg)',
        color: 'var(--center-channel-color)',
        border: '1px solid rgba(var(--center-channel-color-rgb), 0.2)',
        borderRadius: 4,
        cursor: 'pointer',
        fontSize: 14,
        marginRight: 8,
    } as React.CSSProperties,
    btnDanger: {
        padding: '4px 10px',
        backgroundColor: 'var(--error-text)',
        color: '#fff',
        border: 'none',
        borderRadius: 4,
        cursor: 'pointer',
        fontSize: 13,
    } as React.CSSProperties,
    actionItem: {
        border: '1px solid rgba(var(--center-channel-color-rgb), 0.15)',
        borderRadius: 4,
        padding: 12,
        marginBottom: 8,
    } as React.CSSProperties,
    actionHeader: {
        display: 'flex',
        justifyContent: 'space-between',
        alignItems: 'center',
        marginBottom: 8,
    } as React.CSSProperties,
    error: {
        color: 'var(--error-text)',
        marginBottom: 12,
    } as React.CSSProperties,
    warning: {
        padding: '8px 12px',
        borderRadius: 4,
        backgroundColor: 'rgba(var(--error-text-color-rgb, 210, 75, 78), 0.08)',
        color: 'var(--error-text)',
        fontSize: 13,
        marginBottom: 12,
    } as React.CSSProperties,
    header: {
        display: 'flex',
        alignItems: 'center',
        gap: 12,
        marginBottom: 20,
    } as React.CSSProperties,
    actionId: {
        fontSize: 12,
        color: 'rgba(var(--center-channel-color-rgb), 0.56)',
        fontFamily: 'monospace',
        userSelect: 'all',
    } as React.CSSProperties,
    details: {
        marginTop: 8,
        marginBottom: 12,
    } as React.CSSProperties,
    summary: {
        fontSize: 13,
        color: 'rgba(var(--center-channel-color-rgb), 0.56)',
        cursor: 'pointer',
    } as React.CSSProperties,
    pre: {
        fontSize: 12,
        fontFamily: 'monospace',
        backgroundColor: 'rgba(var(--center-channel-color-rgb), 0.05)',
        borderRadius: 4,
        padding: 8,
        overflowX: 'auto',
        margin: '4px 0 0',
    } as React.CSSProperties,
    toolList: {
        border: '1px solid rgba(var(--center-channel-color-rgb), 0.15)',
        borderRadius: 4,
        maxHeight: 200,
        overflowY: 'auto',
        padding: 4,
    } as React.CSSProperties,
    toolItem: {
        display: 'flex',
        alignItems: 'flex-start',
        gap: 6,
        padding: '4px 8px',
        fontSize: 13,
    } as React.CSSProperties,
    toolDescription: {
        fontSize: 12,
        color: 'rgba(var(--center-channel-color-rgb), 0.56)',
    } as React.CSSProperties,
};

const allTriggers = getAllTriggerConfigs();
const defaultTriggerType = allTriggers[0]?.type ?? 'message_posted';

// Reads guardrails.channel_ids from a stored automation as a deduped, trimmed
// string[]. Non-string entries and empty strings are skipped.
function normalizeGuardrailChannelIDs(g: unknown): string[] {
    if (!g || typeof g !== 'object') {
        return [];
    }
    const raw = (g as {channel_ids?: unknown}).channel_ids;
    if (!raw || !Array.isArray(raw)) {
        return [];
    }
    const out: string[] = [];
    const seen = new Set<string>();
    for (const item of raw) {
        if (typeof item !== 'string') {
            continue;
        }
        const n = item.trim();
        if (n !== '' && !seen.has(n)) {
            seen.add(n);
            out.push(n);
        }
    }
    return out;
}

function normalizeAllowedTools(raw: unknown): string[] {
    if (!raw || !Array.isArray(raw)) {
        return [];
    }
    const out: string[] = [];
    const seen = new Set<string>();
    for (const item of raw as Array<string | {name?: string}>) {
        if (typeof item === 'string') {
            const n = item.trim();
            if (n !== '' && !seen.has(n)) {
                seen.add(n);
                out.push(n);
            }
            continue;
        }
        if (item && typeof item === 'object' && typeof item.name === 'string') {
            const n = item.name.trim();
            if (n !== '' && !seen.has(n)) {
                seen.add(n);
                out.push(n);
            }
        }
    }
    return out;
}

function newActionForm(): ActionForm {
    return {id: '', type: 'send_message', channel_id: '', reply_to_post_id: '', as_bot_id: '', body: '', system_prompt: '', prompt: '', provider_id: '', allowed_tool_refs: [], request_as: '', guardrail_channel_ids: []};
}

function actionToForm(a: Action): ActionForm {
    if (a.ai_prompt) {
        return {
            id: a.id,
            type: 'ai_prompt',
            channel_id: '',
            reply_to_post_id: '',
            as_bot_id: '',
            body: '',
            system_prompt: a.ai_prompt.system_prompt ?? '',
            prompt: a.ai_prompt.prompt ?? '',
            provider_id: a.ai_prompt.provider_id ?? '',
            allowed_tool_refs: normalizeAllowedTools(a.ai_prompt.allowed_tools),
            request_as: a.ai_prompt.request_as ?? '',
            guardrail_channel_ids: normalizeGuardrailChannelIDs(a.ai_prompt.guardrails),
        };
    }
    if (a.send_message) {
        return {
            id: a.id,
            type: 'send_message',
            channel_id: a.send_message.channel_id,
            reply_to_post_id: a.send_message.reply_to_post_id ?? '',
            as_bot_id: a.send_message.as_bot_id ?? '',
            body: a.send_message.body,
            system_prompt: '',
            prompt: '',
            provider_id: '',
            allowed_tool_refs: [],
            request_as: '',
            guardrail_channel_ids: [],
        };
    }
    return {id: a.id, type: '', channel_id: '', reply_to_post_id: '', as_bot_id: '', body: '', system_prompt: '', prompt: '', provider_id: '', allowed_tool_refs: [], request_as: '', guardrail_channel_ids: []};
}

const hintStyle: React.CSSProperties = {fontSize: 13, color: 'rgba(var(--center-channel-color-rgb), 0.56)', margin: 0};

interface ToolSelectorProps {
    action: ActionForm;
    index: number;
    tools: AIToolInfo[] | undefined;
    onToggle: (index: number, tool: AIToolInfo, checked: boolean) => void;
}

const ToolSelector: React.FC<ToolSelectorProps> = ({action, index, tools, onToggle}) => {
    if (!action.provider_id) {
        return <p style={hintStyle}>{'Select an agent to see available tools.'}</p>;
    }
    if (tools === undefined) {
        return <p style={hintStyle}>{'Loading tools...'}</p>;
    }
    if (tools.length === 0) {
        return <p style={hintStyle}>{'No tools available for this agent.'}</p>;
    }
    const selected = new Set(action.allowed_tool_refs);
    return (
        <div style={styles.toolList}>
            {tools.map((tool) => (
                <label
                    key={tool.name}
                    style={styles.toolItem}
                >
                    <input
                        type='checkbox'
                        checked={selected.has(tool.name)}
                        onChange={(e) => onToggle(index, tool, e.target.checked)}
                    />
                    <span>
                        <strong>{tool.name}</strong>
                        {tool.description ? (
                            <span style={styles.toolDescription}>{` \u2014 ${tool.description}`}</span>
                        ) : null}
                    </span>
                </label>
            ))}
        </div>
    );
};

const AutomationEditorView: React.FC = () => {
    const {id: automationId} = useParams<{id?: string}>();
    const history = useHistory();
    const {url} = useRouteMatch();

    // Derive the automations list URL by stripping the current route suffix
    const automationsUrl = url.replace(/\/automations\/.*$/, '/automations');

    const [name, setName] = useState('');
    const [enabled, setEnabled] = useState(false);
    const [triggerType, setTriggerType] = useState(defaultTriggerType);
    const [triggerState, setTriggerState] = useState<TriggerFormState>(
        () => getTriggerConfig(defaultTriggerType)?.defaultFormState() ?? {},
    );
    const [actions, setActions] = useState<ActionForm[]>([]);
    const [agents, setAgents] = useState<AIBotInfo[]>([]);
    const [agentsError, setAgentsError] = useState<string | null>(null);
    const [agentTools, setAgentTools] = useState<Map<number, AIToolInfo[]>>(new Map());
    const [loading, setLoading] = useState(Boolean(automationId));
    const [saving, setSaving] = useState(false);
    const [error, setError] = useState<string | null>(null);

    const triggerConfig = getTriggerConfig(triggerType);

    useEffect(() => {
        let cancelled = false;
        getAIBots().then((bots) => {
            if (cancelled) {
                return;
            }
            setAgents(bots);
            if (bots.length === 0) {
                setAgentsError('No AI agents found. Please configure agents in the AI plugin.');
            }
        }).catch((err: unknown) => {
            if (cancelled) {
                return;
            }
            setAgents([]);
            const msg = err instanceof Error ? err.message : 'unknown error';
            setAgentsError(`Could not load AI agents: ${msg}`);
        });
        return () => {
            cancelled = true;
        };
    }, []);

    const fetchToolsForAction = useCallback((index: number, agentId: string) => {
        if (!agentId) {
            setAgentTools((prev) => {
                const next = new Map(prev);
                next.delete(index);
                return next;
            });
            return;
        }
        getAgentTools(agentId).then((tools) => {
            setAgentTools((prev) => {
                const next = new Map(prev);
                next.set(index, tools);
                return next;
            });
        }).catch((err: unknown) => {
            // eslint-disable-next-line no-console
            console.error('Failed to fetch agent tools:', err);
            setAgentTools((prev) => {
                const next = new Map(prev);
                next.set(index, []);
                return next;
            });
        });
    }, []);

    useEffect(() => {
        if (!automationId) {
            return undefined;
        }
        let cancelled = false;
        (async () => {
            try {
                const automation = await getAutomation(automationId);
                if (cancelled) {
                    return;
                }
                setName(automation.name);
                setEnabled(automation.enabled);
                const tt = getTriggerType(automation.trigger);
                setTriggerType(tt);
                const config = getTriggerConfig(tt);
                setTriggerState(config?.fromTrigger(automation.trigger) ?? {});
                const forms = (automation.actions ?? []).map(actionToForm);
                setActions(forms);
                forms.forEach((a, idx) => {
                    if (a.type === 'ai_prompt' && a.provider_id) {
                        fetchToolsForAction(idx, a.provider_id);
                    }
                });
            } catch (err: unknown) {
                if (!cancelled) {
                    setError(err instanceof Error ? err.message : 'Failed to load automation');
                }
            } finally {
                if (!cancelled) {
                    setLoading(false);
                }
            }
        })();
        return () => {
            cancelled = true;
        };
    }, [automationId, fetchToolsForAction]);

    const handleTriggerTypeChange = useCallback((newType: string) => {
        setTriggerType(newType);
        const config = getTriggerConfig(newType);
        setTriggerState(config?.defaultFormState() ?? {});
    }, []);

    const handleTriggerFieldChange = useCallback((field: string, value: string) => {
        setTriggerState((prev) => ({...prev, [field]: value}));
    }, []);

    const handleAddAction = useCallback(() => {
        setActions((prev) => [...prev, newActionForm()]);
    }, []);

    const handleRemoveAction = useCallback((index: number) => {
        setActions((prev) => prev.filter((_, i) => i !== index));
        setAgentTools((prev) => {
            const next = new Map<number, AIToolInfo[]>();
            for (const [k, v] of prev) {
                if (k < index) {
                    next.set(k, v);
                } else if (k > index) {
                    next.set(k - 1, v);
                }
            }
            return next;
        });
    }, []);

    const handleActionChange = useCallback((index: number, field: keyof ActionForm, value: string) => {
        setActions((prev) => prev.map((a, i) => (i === index ? {...a, [field]: value} : a)));
    }, []);

    const handleAgentChange = useCallback((index: number, agentId: string) => {
        handleActionChange(index, 'provider_id', agentId);
        fetchToolsForAction(index, agentId);
    }, [handleActionChange, fetchToolsForAction]);

    const handleToolToggle = useCallback((index: number, tool: AIToolInfo, checked: boolean) => {
        setActions((prev) => prev.map((a, i) => {
            if (i !== index) {
                return a;
            }
            const next = a.allowed_tool_refs.filter((name) => name !== tool.name);
            if (checked) {
                return {...a, allowed_tool_refs: [...next, tool.name]};
            }
            return {...a, allowed_tool_refs: next};
        }));
    }, []);

    const handleGuardrailChannelsInput = useCallback((index: number, raw: string) => {
        const parts = Array.from(new Set(
            raw.split(/[\n,]+/).map((s) => s.trim()).filter((s) => s.length > 0),
        ));
        setActions((prev) => prev.map((a, i) => (i === index ? {...a, guardrail_channel_ids: parts} : a)));
    }, []);

    const handleSave = useCallback(async () => {
        setSaving(true);
        setError(null);

        const trigger = triggerConfig?.toTrigger(triggerState) ?? {};

        const data = {
            name,
            enabled,
            trigger,
            actions: actions.map((a): Action => {
                if (a.type === 'ai_prompt') {
                    const action: Action = {
                        id: a.id,
                        ai_prompt: {
                            prompt: a.prompt,
                            provider_type: 'agent',
                            provider_id: a.provider_id,
                        },
                    };
                    if (a.system_prompt.trim() && action.ai_prompt) {
                        action.ai_prompt.system_prompt = a.system_prompt;
                    }
                    if (a.allowed_tool_refs.length > 0 && action.ai_prompt) {
                        action.ai_prompt.allowed_tools = a.allowed_tool_refs;
                    }

                    // Creator-locked triggers require request_as=creator server-side;
                    // mirror that here so the saved config matches enforced behavior.
                    const creatorLocked = triggerType === 'membership_changed' ||
                        triggerType === 'user_joined_team' ||
                        triggerType === 'channel_created';
                    const requestAs = creatorLocked ? 'creator' : a.request_as;
                    if (requestAs && action.ai_prompt) {
                        action.ai_prompt.request_as = requestAs;
                    }
                    if (a.guardrail_channel_ids.length > 0 && action.ai_prompt) {
                        action.ai_prompt.guardrails = {channel_ids: [...a.guardrail_channel_ids]};
                    }
                    return action;
                }
                const action: Action = {
                    id: a.id,
                    send_message: {
                        channel_id: a.channel_id,
                        body: a.body,
                    },
                };
                if (a.reply_to_post_id && action.send_message) {
                    action.send_message.reply_to_post_id = a.reply_to_post_id;
                }
                if (a.as_bot_id && action.send_message) {
                    action.send_message.as_bot_id = a.as_bot_id;
                }
                return action;
            }),
        };
        try {
            if (automationId) {
                await updateAutomation(automationId, data);
            } else {
                await createAutomation(data);
            }
            history.push(automationsUrl);
        } catch (err: unknown) {
            setError(err instanceof Error ? err.message : 'Failed to save automation');
            setSaving(false);
        }
    }, [automationId, name, enabled, triggerType, triggerState, triggerConfig, actions, history, automationsUrl]);

    if (loading) {
        return <p>{'Loading...'}</p>;
    }

    return (
        <div>
            <div style={styles.header}>
                <button
                    style={styles.btnSecondary}
                    onClick={() => history.push(automationsUrl)}
                >
                    {'\u2190 Back'}
                </button>
                <h2>{automationId ? 'Edit Automation' : 'Create Automation'}</h2>
            </div>
            {error && <p style={styles.error}>{error}</p>}
            <div style={styles.formGroup}>
                <label
                    htmlFor='automation-name'
                    style={styles.label}
                >{'Name'}</label>
                <input
                    id='automation-name'
                    style={styles.input}
                    type='text'
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                />
            </div>
            <div style={styles.formGroup}>
                <label style={styles.label}>
                    <input
                        type='checkbox'
                        checked={enabled}
                        onChange={(e) => setEnabled(e.target.checked)}
                    />
                    {' Enabled'}
                </label>
            </div>
            <h3>{'Trigger'}</h3>
            {triggerConfig && (
                <details style={styles.details}>
                    <summary style={styles.summary}>{'Available template variables'}</summary>
                    <pre style={styles.pre}>{triggerConfig.templateVariables}</pre>
                </details>
            )}
            <div style={styles.formGroup}>
                <label
                    htmlFor='trigger-type'
                    style={styles.label}
                >{'Type'}</label>
                <select
                    id='trigger-type'
                    style={styles.select}
                    value={triggerType}
                    onChange={(e) => handleTriggerTypeChange(e.target.value)}
                >
                    {allTriggers.map((t) => (
                        <option
                            key={t.type}
                            value={t.type}
                        >
                            {t.label}
                        </option>
                    ))}
                </select>
            </div>
            {triggerConfig?.renderFields(triggerState, handleTriggerFieldChange, styles)}
            <h3>{'Actions'}</h3>
            {actions.map((action, index) => (
                <div
                    key={index}
                    style={styles.actionItem}
                >
                    <div style={styles.actionHeader}>
                        <strong>{`Action ${index + 1}`}</strong>
                        <button
                            style={styles.btnDanger}
                            onClick={() => handleRemoveAction(index)}
                        >
                            {'Remove'}
                        </button>
                    </div>
                    <div style={styles.formGroup}>
                        <label
                            htmlFor={`action-${index}-id`}
                            style={styles.label}
                        >{'ID'}</label>
                        <input
                            id={`action-${index}-id`}
                            style={styles.input}
                            type='text'
                            placeholder='e.g. send-greeting'
                            value={action.id}
                            onChange={(e) => handleActionChange(index, 'id', e.target.value)}
                        />
                    </div>
                    <div style={styles.formGroup}>
                        <label
                            htmlFor={`action-${index}-type`}
                            style={styles.label}
                        >{'Type'}</label>
                        <select
                            id={`action-${index}-type`}
                            style={styles.select}
                            value={action.type}
                            onChange={(e) => handleActionChange(index, 'type', e.target.value)}
                        >
                            <option value='send_message'>{'send_message'}</option>
                            <option value='ai_prompt'>{'ai_prompt'}</option>
                        </select>
                    </div>
                    {action.type === 'send_message' && (
                        <>
                            <div style={styles.formGroup}>
                                <label
                                    htmlFor={`action-${index}-channel-id`}
                                    style={styles.label}
                                >{'Channel ID'}</label>
                                <input
                                    id={`action-${index}-channel-id`}
                                    style={styles.input}
                                    type='text'
                                    value={action.channel_id}
                                    onChange={(e) => handleActionChange(index, 'channel_id', e.target.value)}
                                />
                            </div>
                            <div style={styles.formGroup}>
                                <label
                                    htmlFor={`action-${index}-reply-to-post-id`}
                                    style={styles.label}
                                >{'Reply to Post ID (Go template, optional)'}</label>
                                <input
                                    id={`action-${index}-reply-to-post-id`}
                                    style={styles.input}
                                    type='text'
                                    value={action.reply_to_post_id}
                                    placeholder={'e.g. {{.Trigger.Post.ThreadId}}'}
                                    onChange={(e) => handleActionChange(index, 'reply_to_post_id', e.target.value)}
                                />
                            </div>
                            <div style={styles.formGroup}>
                                <label
                                    htmlFor={`action-${index}-as-bot-id`}
                                    style={styles.label}
                                >{'Bot User ID (optional, overrides default bot)'}</label>
                                <input
                                    id={`action-${index}-as-bot-id`}
                                    style={styles.input}
                                    type='text'
                                    value={action.as_bot_id}
                                    placeholder={'Bot user ID'}
                                    onChange={(e) => handleActionChange(index, 'as_bot_id', e.target.value)}
                                />
                            </div>
                            <div style={styles.formGroup}>
                                <label
                                    htmlFor={`action-${index}-body`}
                                    style={styles.label}
                                >{'Body (Go template)'}</label>
                                <textarea
                                    id={`action-${index}-body`}
                                    style={styles.textarea}
                                    value={action.body}
                                    onChange={(e) => handleActionChange(index, 'body', e.target.value)}
                                />
                            </div>
                            <details style={styles.details}>
                                <summary style={styles.summary}>{'Output of this action'}</summary>
                                <pre style={styles.pre}>{`{
  "PostID": "string",
  "ChannelID": "string",
  "Message": "string"
}

Usage: {{(index .Steps "${action.id || '<action_id>'}").Message}}`}</pre>
                            </details>
                        </>
                    )}
                    {action.type === 'ai_prompt' && (
                        <>
                            {agentsError && <p style={styles.warning}>{agentsError}</p>}
                            <div style={styles.formGroup}>
                                <label
                                    htmlFor={`action-${index}-agent`}
                                    style={styles.label}
                                >{'Agent'}</label>
                                <select
                                    id={`action-${index}-agent`}
                                    style={styles.select}
                                    value={action.provider_id}
                                    onChange={(e) => handleAgentChange(index, e.target.value)}
                                >
                                    <option value=''>{'Select an agent...'}</option>
                                    {agents.map((bot) => (
                                        <option
                                            key={bot.id}
                                            value={bot.id}
                                        >
                                            {`${bot.display_name} (@${bot.username})`}
                                        </option>
                                    ))}
                                </select>
                            </div>
                            <div style={styles.formGroup}>
                                <label
                                    htmlFor={`action-${index}-system-prompt`}
                                    style={styles.label}
                                >{'System Prompt (Go template, optional)'}</label>
                                <textarea
                                    id={`action-${index}-system-prompt`}
                                    style={styles.textarea}
                                    placeholder={'e.g. You are a helpful assistant.'}
                                    value={action.system_prompt}
                                    onChange={(e) => handleActionChange(index, 'system_prompt', e.target.value)}
                                />
                            </div>
                            <div style={styles.formGroup}>
                                <label
                                    htmlFor={`action-${index}-prompt`}
                                    style={styles.label}
                                >{'Prompt (Go template)'}</label>
                                <textarea
                                    id={`action-${index}-prompt`}
                                    style={styles.textarea}
                                    value={action.prompt}
                                    onChange={(e) => handleActionChange(index, 'prompt', e.target.value)}
                                />
                            </div>
                            <div style={styles.formGroup}>
                                <label style={styles.label}>{'Allowed Tools (optional)'}</label>
                                <ToolSelector
                                    action={action}
                                    index={index}
                                    tools={agentTools.get(index)}
                                    onToggle={handleToolToggle}
                                />
                            </div>
                            <div style={styles.formGroup}>
                                <label
                                    htmlFor={`action-${index}-request-as`}
                                    style={styles.label}
                                >{'Request as'}</label>
                                <select
                                    id={`action-${index}-request-as`}
                                    style={styles.select}
                                    value={(triggerType === 'membership_changed' || triggerType === 'user_joined_team' || triggerType === 'channel_created') ? 'creator' : action.request_as}
                                    disabled={triggerType === 'membership_changed' || triggerType === 'user_joined_team' || triggerType === 'channel_created'}
                                    onChange={(e) => handleActionChange(index, 'request_as', e.target.value)}
                                >
                                    <option value=''>{'Triggerer (default)'}</option>
                                    <option value='triggerer'>{'Triggerer'}</option>
                                    <option value='creator'>{'Creator'}</option>
                                </select>
                                <p style={hintStyle}>{(triggerType === 'membership_changed' || triggerType === 'user_joined_team' || triggerType === 'channel_created') ? 'This trigger always runs as the automation creator, because the triggerer is an incidental subject of the event (joiner, leaver, or channel creator).' : 'Selects which user the AI request is attributed to. "Triggerer" uses the user who triggered the automation (falling back to the creator). "Creator" always uses the automation creator.'}</p>
                            </div>
                            {action.allowed_tool_refs.length > 0 && (
                                <div style={styles.formGroup}>
                                    <label
                                        htmlFor={`action-${index}-guardrail-channels`}
                                        style={styles.label}
                                    >{'Guardrail channel IDs (optional)'}</label>
                                    <p style={hintStyle}>
                                        {'When set, MCP tool calls from this step are limited to these channels. One 26-character Mattermost channel ID per line (or comma-separated). Requires at least one allowed tool.'}
                                    </p>
                                    <textarea
                                        id={`action-${index}-guardrail-channels`}
                                        style={styles.textarea}
                                        rows={4}
                                        placeholder={'e.g.\nh5wqm8kxptbztfgzpaxbsqozah'}
                                        value={action.guardrail_channel_ids.join('\n')}
                                        onChange={(e) => handleGuardrailChannelsInput(index, e.target.value)}
                                    />
                                </div>
                            )}
                            <details style={styles.details}>
                                <summary style={styles.summary}>{'Output of this action'}</summary>
                                <pre style={styles.pre}>{`{
  "Message": "string"
}

Usage: {{(index .Steps "${action.id || '<action_id>'}").Message}}`}</pre>
                            </details>
                        </>
                    )}
                </div>
            ))}
            <div style={{marginBottom: 20}}>
                <button
                    style={styles.btnSecondary}
                    onClick={handleAddAction}
                >
                    {'+ Add Action'}
                </button>
            </div>
            <div>
                <button
                    style={styles.btnPrimary}
                    onClick={handleSave}
                    disabled={saving}
                >
                    {saving ? 'Saving...' : 'Save'}
                </button>
                <button
                    style={styles.btnSecondary}
                    onClick={() => history.push(automationsUrl)}
                >
                    {'Cancel'}
                </button>
            </div>
        </div>
    );
};

export default AutomationEditorView;
