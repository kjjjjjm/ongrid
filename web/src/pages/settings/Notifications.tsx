import { Input, Label, Textarea } from '@/components/ui';
import { Hint } from '@/components/ui/Tooltip';
import { Checkbox } from '@/components/ui/Checkbox';
import { Select } from '@/components/ui/Select';
import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Bell,
  ChevronDown,
  ChevronRight,
  Loader2,
  Webhook,
  Plus,
  Pencil,
  Trash2,
} from 'lucide-react';
import { Modal } from '@/components/Modal';
import { SettingsProviderPicker } from '@/components/SettingsProviderPicker';
import { Button, Card, Chip } from '@/components/ui';
import { cn } from '@/lib/cn';
import {
  createChannel,
  deleteChannel,
  getChannelTemplateStarter,
  listChannels,
  previewChannelTemplate,
  testChannel,
  updateChannel,
  type Channel,
  type ChannelInput,
  type ChannelMessageFormat,
  type ChannelTemplatePreview,
  type ChannelTestResult,
} from '@/api/alerts';
import { ApiError } from '@/api/client';
import { CommunicationProviderIcon } from '@/components/icons/Provider';
import { useI18n } from '@/i18n/locale';

// One notification type ↔ one card. Order = display order on the page.
//
// The legacy "log" channel type was removed in 2026-05: manager
// stdout is ephemeral and the alert_events table already records
// every notification attempt with status — that's the audit trail.
// Operators looking for delivery history should hit
// 设置 → 告警事件 instead.
type ChannelType = 'webhook' | 'slack' | 'feishu' | 'dingtalk' | 'wecom' | 'telegram';

type TypeMeta = {
  type: ChannelType;
  group: 'cn' | 'intl' | 'generic';
  labelZh: string;
  labelEn: string;
  hintZh: string;
  hintEn: string;
  endpointPlaceholder: string;
};

const TYPE_CARDS: TypeMeta[] = [
  {
    type: 'feishu',
    group: 'cn',
    labelZh: '飞书',
    labelEn: 'Larksuite',
    hintZh: '飞书自定义机器人 webhook，支持签名校验。',
    hintEn: 'Larksuite custom-bot webhook; supports signature verification.',
    endpointPlaceholder: 'https://open.feishu.cn/open-apis/bot/v2/hook/xxxx',
  },
  {
    type: 'dingtalk',
    group: 'cn',
    labelZh: '钉钉',
    labelEn: 'DingTalk',
    hintZh: '钉钉自定义机器人 webhook，支持加签 secret。',
    hintEn: 'DingTalk custom-bot webhook; supports signature secret.',
    endpointPlaceholder: 'https://oapi.dingtalk.com/robot/send?access_token=xxxx',
  },
  {
    type: 'wecom',
    group: 'cn',
    labelZh: '企业微信',
    labelEn: 'WeCom',
    hintZh: '企业微信群机器人 webhook（key 在 URL 里）。',
    hintEn: 'WeCom group-bot webhook (key embedded in the URL).',
    endpointPlaceholder: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxxx',
  },
  {
    type: 'slack',
    group: 'intl',
    labelZh: 'Slack',
    labelEn: 'Slack',
    hintZh: 'Slack incoming webhook（用于工作区频道）。',
    hintEn: 'Slack incoming webhook (for workspace channels).',
    endpointPlaceholder: 'https://hooks.slack.com/services/Txxx/Bxxx/xxx',
  },
  {
    type: 'telegram',
    group: 'intl',
    labelZh: 'Telegram',
    labelEn: 'Telegram',
    hintZh: 'Telegram bot sendMessage 接口；endpoint 填 https://api.telegram.org/bot<token>/sendMessage，secret 字段填 Chat ID。',
    hintEn: 'Telegram bot sendMessage API; endpoint = https://api.telegram.org/bot<token>/sendMessage, put the Chat ID in the secret field.',
    endpointPlaceholder: 'https://api.telegram.org/bot<token>/sendMessage',
  },
  {
    type: 'webhook',
    group: 'generic',
    labelZh: 'Webhook',
    labelEn: 'Webhook',
    hintZh: '通用 JSON webhook；secret 设置后会附 X-Ongrid-Signature。',
    hintEn: 'Generic JSON webhook; if a secret is set the request carries X-Ongrid-Signature.',
    endpointPlaceholder: 'https://example.com/ingest',
  },
];

// Locale-aware ordering: surface the channels most relevant to the UI
// language first. English → Slack / Telegram first; Chinese → 飞书 / 钉钉 /
// 企业微信 first. Webhook (generic) stays last either way. Within a group the
// declaration order above is preserved (Array.prototype.sort is stable).
const GROUP_RANK: Record<string, Record<TypeMeta['group'], number>> = {
  'en-US': { intl: 0, cn: 1, generic: 2 },
  'zh-CN': { cn: 0, intl: 1, generic: 2 },
};
function orderCardsByLocale(locale: string): TypeMeta[] {
  const rank = GROUP_RANK[locale] ?? GROUP_RANK['zh-CN'];
  return [...TYPE_CARDS].sort((a, b) => rank[a.group] - rank[b.group]);
}

// IM bots with both a text and a Markdown message type.
const MARKDOWN_TYPES: ReadonlySet<string> = new Set(['wecom', 'dingtalk', 'feishu']);

function hasTemplate(ch: Channel): boolean {
  return Boolean(ch.format || ch.title_template || ch.content_template);
}

function ChannelTypeIcon({ type, size }: { type: ChannelType; size: number }) {
  return type === 'webhook'
    ? <Webhook size={size} className="text-zinc-400" />
    : <CommunicationProviderIcon provider={type} size={size} />;
}

type Toast = { kind: 'ok' | 'err'; text: string } | null;

export default function SettingsNotifications() {
  const { tr, locale } = useI18n();
  const orderedCards = useMemo(() => orderCardsByLocale(locale), [locale]);
  const [items, setItems] = useState<Channel[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [editing, setEditing] = useState<{
    mode: 'create' | 'edit';
    type?: ChannelType;
    channel?: Channel;
  } | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<Channel | null>(null);
  const [testingId, setTestingId] = useState<number | null>(null);
  const [testResult, setTestResult] = useState<{ id: number; result: ChannelTestResult } | null>(null);
  const [toast, setToast] = useState<Toast>(null);

  const fetchChannels = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const r = await listChannels();
      setItems(r.items ?? []);
      setErr(null);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      if (!silent) setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchChannels();
  }, [fetchChannels]);

  useEffect(() => {
    if (!toast) return;
    const t = setTimeout(() => setToast(null), 4000);
    return () => clearTimeout(t);
  }, [toast]);

  const handleTest = useCallback(async (channel: Channel) => {
    setTestingId(channel.id);
    setTestResult(null);
    try {
      const r = await testChannel(channel.id);
      setTestResult({ id: channel.id, result: r });
      if (r.accepted) {
        setToast({ kind: 'ok', text: tr(`已向 ${channel.name} 投递测试消息`, `Test message sent to ${channel.name}`) });
      } else {
        setToast({ kind: 'err', text: tr(`投递失败：${r.message ?? '未知错误'}`, `Delivery failed: ${r.message ?? 'unknown error'}`) });
      }
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : (e as Error).message;
      setToast({ kind: 'err', text: tr(`投递失败：${msg}`, `Delivery failed: ${msg}`) });
    } finally {
      setTestingId(null);
    }
  }, [tr]);

  const grouped = useMemo(() => {
    const m = new Map<string, Channel[]>();
    for (const c of items) {
      const list = m.get(c.type) ?? [];
      list.push(c);
      m.set(c.type, list);
    }
    return m;
  }, [items]);
  const configuredCards = orderedCards.filter((meta) => grouped.has(meta.type));

  return (
    <>
      {/* Layout mirrors Settings → 模型 (LLM) and Settings → 集成:
          a thin intro panel at the top explaining what this page is,
          then one Card per channel-type with p-5 / 14-px icon / 11-px
          description / bottom action row. Each channel-type is
          treated like its own integration; the channel list slots in
          between description and action row. */}
      <div className="space-y-4">
        <div className="rounded-lg border border-zinc-800/60 bg-zinc-900/30 px-4 py-3 text-[12px] text-zinc-400">
          <div className="mb-1 flex items-center gap-2 text-zinc-200">
            <Bell size={14} className="text-zinc-400" />
            <span className="font-medium">{tr('通知渠道', 'Notification channels')}</span>
          </div>
          {tr('告警 / 巡检 / 主动 AIOps 的单向外部落点。env 变量 ', 'One-way external sinks for alerts / inspections / proactive AIOps. The env var ')}
          <code className="font-mono text-zinc-300">ONGRID_NOTIFY_*</code>
          {tr(' 启动时同步入库；UI 改动持久化到 DB，下次启动 env 不会覆盖。投递记录（成功 / 失败 / 重试）请查 ', ' is synced into the DB at startup; UI edits are persisted and not overwritten by env on next boot. Delivery records (success / failure / retry) live under ')}
          <b>{tr('设置 → 告警事件', 'Settings → Alert events')}</b>
          {tr('。需要双向 bot 会话请看 ', '. For two-way bot sessions, see ')}
          <b>{tr('设置 → IM 机器人', 'Settings → IM bots')}</b>{tr('。', '.')}
        </div>

        {err && (
          <Card className="p-4">
            <div className="text-sm text-red-300">
              {tr('加载失败：', 'Load failed: ')}{err}
            </div>
          </Card>
        )}

        {loading ? (
          <Card className="p-5">
            <div className="flex h-32 items-center justify-center text-sm text-zinc-500">
              <Loader2 size={14} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
            </div>
          </Card>
        ) : (
          <>
            <SettingsProviderPicker
              testId="notification-provider-picker"
              label={tr('添加通知渠道', 'Add notification channel')}
              summary={tr(`${orderedCards.length} 种可选`, `${orderedCards.length} available`)}
              options={orderedCards.map((meta) => {
                const count = grouped.get(meta.type)?.length ?? 0;
                return {
                  id: meta.type,
                  icon: <ChannelTypeIcon type={meta.type} size={18} />,
                  label: tr(meta.labelZh, meta.labelEn),
                  description: tr(meta.hintZh, meta.hintEn),
                  meta: count > 0 ? tr(`已配置 ${count} 个`, `${count} configured`) : tr('未配置', 'Not configured'),
                };
              })}
              onSelect={(type) => setEditing({ mode: 'create', type })}
            />
            {configuredCards.map((meta) => (
              <TypeCard
                key={meta.type}
                meta={meta}
                channels={grouped.get(meta.type) ?? []}
                testingId={testingId}
                testResult={testResult}
                onAdd={() => setEditing({ mode: 'create', type: meta.type })}
                onTest={(ch) => handleTest(ch)}
                onEdit={(ch) => setEditing({ mode: 'edit', type: ch.type as ChannelType, channel: ch })}
                onDelete={(ch) => setConfirmDelete(ch)}
              />
            ))}
          </>
        )}
      </div>

      {editing && (
        <ChannelEditorModal
          mode={editing.mode}
          presetType={editing.type}
          channel={editing.channel}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            fetchChannels(true);
            setToast({ kind: 'ok', text: editing.mode === 'create' ? tr('渠道已创建', 'Channel created') : tr('渠道已更新', 'Channel updated') });
          }}
        />
      )}

      {confirmDelete && (
        <Modal
          open
          onClose={() => setConfirmDelete(null)}
          title={tr('删除通知渠道', 'Delete notification channel')}
          footer={
            <>
              <Button onClick={() => setConfirmDelete(null)} variant="ghost">
                {tr('取消', 'Cancel')}
              </Button>
              <Button
                variant="danger"
                onClick={async () => {
                  try {
                    await deleteChannel(confirmDelete.id);
                    setConfirmDelete(null);
                    fetchChannels(true);
                    setToast({ kind: 'ok', text: tr(`已删除 ${confirmDelete.name}`, `Deleted ${confirmDelete.name}`) });
                  } catch (e) {
                    const msg = e instanceof ApiError ? e.message : (e as Error).message;
                    setToast({ kind: 'err', text: tr(`删除失败：${msg}`, `Delete failed: ${msg}`) });
                  }
                }}
              >
                {tr('删除', 'Delete')}
              </Button>
            </>
          }
        >
          <div className="text-sm text-zinc-300">
            {tr('确定要删除渠道 ', 'Delete channel ')}
            <span className="font-mono text-zinc-100">{confirmDelete.name}</span>
            {tr(' 吗？删除后将不再投递新告警，已投递历史记录保留。', '? It will stop receiving new alerts; existing delivery history is retained.')}
          </div>
        </Modal>
      )}

      {toast && (
        <div
          className={cn(
            'fixed bottom-6 right-6 z-50 max-w-sm rounded-lg px-4 py-2.5 text-sm shadow-2xl ring-1 ring-inset',
            toast.kind === 'ok'
              ? 'bg-emerald-500/15 text-emerald-200 ring-emerald-500/40'
              : 'bg-red-500/15 text-red-200 ring-red-500/40'
          )}
        >
          {toast.text}
        </div>
      )}
    </>
  );
}

function TypeCard({
  meta,
  channels,
  testingId,
  testResult,
  onAdd,
  onTest,
  onEdit,
  onDelete,
}: {
  meta: TypeMeta;
  channels: Channel[];
  testingId: number | null;
  testResult: { id: number; result: ChannelTestResult } | null;
  onAdd(): void;
  onTest(ch: Channel): void;
  onEdit(ch: Channel): void;
  onDelete(ch: Channel): void;
}) {
  const { tr } = useI18n();
  return (
    <Card className="p-5" data-testid={`notification-provider-${meta.type}`}>
      <div className="mb-3 flex items-center gap-2">
        <ChannelTypeIcon type={meta.type} size={14} />
        <h2 className="text-sm font-medium text-zinc-100">{tr(meta.labelZh, meta.labelEn)}</h2>
        <Chip>{tr(`${channels.length} 个`, `${channels.length} configured`)}</Chip>
      </div>
      <p className="mb-4 text-[11px] text-zinc-500">{tr(meta.hintZh, meta.hintEn)}</p>

      <ul className="divide-y divide-zinc-800/60 overflow-hidden rounded-lg border border-zinc-800 bg-zinc-950/40">
        {channels.map((ch) => (
          <li key={ch.id} className={cn('px-4 py-2.5', !ch.enabled && 'opacity-60')}>
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-medium text-zinc-100">{ch.name}</span>
                  <Chip tone={ch.enabled ? 'success' : 'default'}>
                    {ch.enabled ? tr('已启用', 'Enabled') : tr('已停用', 'Disabled')}
                  </Chip>
                  {hasTemplate(ch) && (
                    <Chip tone="info">
                      {ch.format === 'markdown' ? tr('自定义模板 · Markdown', 'Custom template · Markdown') : tr('自定义模板', 'Custom template')}
                    </Chip>
                  )}
                </div>
                {ch.endpoint && (
                  <div className="mt-1 truncate font-mono text-[11px] text-zinc-500">
                    {ch.endpoint}
                  </div>
                )}
                {testResult?.id === ch.id && !testResult.result.accepted && testResult.result.message && (
                  <div className="mt-1.5 rounded border border-red-500/40 bg-red-500/5 px-2 py-1 text-[11px] text-red-300">
                    {tr('投递失败：', 'Delivery failed: ')}{testResult.result.message}
                  </div>
                )}
              </div>
              <div className="flex shrink-0 items-center gap-1.5">
                <Button
                  onClick={() => onTest(ch)}
                  disabled={testingId === ch.id}
                  variant="ghost"
                >
                  {testingId === ch.id ? tr('投递中…', 'Sending…') : tr('测试', 'Test')}
                </Button>
                <Button onClick={() => onEdit(ch)} variant="ghost">
                  <Pencil size={11} /> {tr('编辑', 'Edit')}
                </Button>
                <Hint content={tr('删除', 'Delete')}><Button
                  onClick={() => onDelete(ch)}
                  aria-label={tr('删除', 'Delete')}

                  variant="dangerGhost"
                  className="px-2"
                >
                  <Trash2 size={12} />
                </Button></Hint>
              </div>
            </div>
          </li>
        ))}
      </ul>

      <div className="mt-4">
        <Button onClick={onAdd} variant="ghost">
          <Plus size={14} />
          <span>{tr(`再添加一个${meta.labelZh}渠道`, `Add another ${meta.labelEn} channel`)}</span>
        </Button>
      </div>
    </Card>
  );
}

function ChannelEditorModal({
  mode,
  presetType,
  channel,
  onClose,
  onSaved,
}: {
  mode: 'create' | 'edit';
  presetType?: ChannelType;
  channel?: Channel;
  onClose(): void;
  onSaved(): void;
}) {
  const { tr } = useI18n();
  const [form, setForm] = useState<ChannelInput>(() => ({
    name: channel?.name ?? '',
    type: channel?.type ?? presetType ?? 'webhook',
    endpoint: channel?.endpoint ?? '',
    secret: '',
    enabled: channel?.enabled ?? true,
    format: channel?.format ?? '',
    title_template: channel?.title_template ?? '',
    content_template: channel?.content_template ?? '',
  }));
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const endpointTruncated = useMemo(
    () => mode === 'edit' && (channel?.endpoint?.endsWith('...') ?? false),
    [mode, channel?.endpoint]
  );

  const typeLabel = (() => {
    const m = TYPE_CARDS.find((t) => t.type === form.type);
    return m ? tr(m.labelZh, m.labelEn) : form.type;
  })();
  const typeMeta = TYPE_CARDS.find((t) => t.type === form.type);

  const submit = async () => {
    if (!form.name.trim()) {
      setErr(tr('请填写渠道名称', 'Please enter a channel name'));
      return;
    }
    if (!form.type.trim()) {
      setErr(tr('请选择渠道类型', 'Please pick a channel type'));
      return;
    }
    if (!form.endpoint.trim()) {
      setErr(tr('请填写 endpoint URL', 'Please enter an endpoint URL'));
      return;
    }
    setSubmitting(true);
    setErr(null);
    // Always send the template fields so clearing them in the form clears
    // them on the server; markdown only applies to the bots that have it.
    const payload: ChannelInput = {
      ...form,
      format: MARKDOWN_TYPES.has(form.type) ? form.format ?? '' : '',
      title_template: form.title_template ?? '',
      content_template: form.content_template ?? '',
    };
    try {
      if (mode === 'create') await createChannel(payload);
      else if (channel) await updateChannel(channel.id, payload);
      onSaved();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={mode === 'create' ? tr(`新建 ${typeLabel} 渠道`, `New ${typeLabel} channel`) : tr(`编辑：${channel?.name}`, `Edit: ${channel?.name}`)}
      footer={
        <>
          <Button onClick={onClose} variant="ghost">
            {tr('取消', 'Cancel')}
          </Button>
          <Button onClick={submit} disabled={submitting} variant="primary">
            {submitting ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
          </Button>
        </>
      }
    >
      <div className="space-y-3 text-sm">
        <Field label={tr('名称', 'Name')}>
          <Input
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
            placeholder={`primary-${form.type}`}
            className="w-full"
          />
        </Field>
        <Field label={tr('类型', 'Type')}>
          <div className="flex min-h-8 items-center gap-2 rounded-md border border-zinc-800 bg-zinc-950 px-2.5 py-1.5 text-xs text-zinc-200">
            {typeMeta ? <ChannelTypeIcon type={typeMeta.type} size={14} /> : null}
            <span>{typeLabel}</span>
          </div>
        </Field>
        <Field label="Endpoint URL">
          <Input
            value={form.endpoint}
            onChange={(e) => setForm({ ...form, endpoint: e.target.value })}
            placeholder={
              TYPE_CARDS.find((t) => t.type === form.type)?.endpointPlaceholder ?? ''
            }
            className="w-full font-mono"
          />
          {endpointTruncated && (
            <div className="mt-1 text-[11px] text-amber-400">
              {tr(
                '当前显示的 endpoint 是脱敏值（结尾有省略号）。保持不变即保留原地址；要更换时清空后粘贴完整地址。',
                'The endpoint shown is masked (ends with an ellipsis). Leave it unchanged to keep the stored URL; to change it, clear the field and paste the full URL.',
              )}
            </div>
          )}
        </Field>
        <Field label={tr('Secret（可选，签名/验签用）', 'Secret (optional, for signing / verification)')}>
          <Input
            type="password"
            value={form.secret ?? ''}
            onChange={(e) => setForm({ ...form, secret: e.target.value })}
            placeholder={
              mode === 'edit'
                ? tr('留空保留旧值；输入 - 表示清除', 'Leave empty to keep the existing value; enter - to clear')
                : tr('可选：签名密钥 / 验签 token', 'Optional: signing key / verification token')
            }
            className="w-full font-mono"
          />
          {(form.type === 'slack' || form.type === 'wecom') && (
            <div className="mt-1 text-[11px] text-zinc-500">
              {form.type === 'slack'
                ? tr(
                    'Slack incoming webhook 的凭证就是 endpoint URL 本身，secret 字段会被忽略，可以留空。',
                    'Slack incoming webhooks use the URL itself as the credential; the secret field is ignored and can be left blank.',
                  )
                : tr(
                    '企业微信群机器人 key 已经在 endpoint URL 里，secret 字段会被忽略，可以留空。',
                    'WeCom group-bot keys are embedded in the endpoint URL; the secret field is ignored and can be left blank.',
                  )}
            </div>
          )}
        </Field>
        <Label className="inline-flex items-center gap-2 text-xs text-zinc-300">
          <Checkbox

            checked={form.enabled}
            onCheckedChange={(checkedValue) => setForm({ ...form, enabled: checkedValue })}
            className="h-3.5 w-3.5"
          />
          {tr('启用此渠道', 'Enable this channel')}
        </Label>
        <TemplateEditor
          type={form.type}
          format={form.format ?? ''}
          titleTemplate={form.title_template ?? ''}
          contentTemplate={form.content_template ?? ''}
          defaultOpen={channel ? hasTemplate(channel) : false}
          onChange={(patch) => setForm((current) => ({ ...current, ...patch }))}
        />
        {err && <div className="text-xs text-red-400">{err}</div>}
      </div>
    </Modal>
  );
}

const TEMPLATE_VARIABLES = [
  '.Title', '.Severity', '.Status', '.RuleName', '.RuleKey', '.Value', '.Threshold', '.DeviceName', '.DeviceIP',
  '.Labels.<name>', '.Annotations.<name>', '.Source', '.EventCount', '.FiredAt', '.OccurredAt', '.IncidentURL', '.RunbookURL',
];
const TEMPLATE_FUNCTIONS = ['upper', 'lower', 'trim', 'replace', 'contains', 'truncate', 'default', 'json', 'date', 'dateInZone'];

// TemplateEditor edits the optional notification template of one channel
// (Go text/template). Empty fields keep the platform's built-in format; the
// preview renders a sample alert on the server without sending anything.
function TemplateEditor({
  type,
  format,
  titleTemplate,
  contentTemplate,
  defaultOpen,
  onChange,
}: {
  type: string;
  format: ChannelMessageFormat;
  titleTemplate: string;
  contentTemplate: string;
  defaultOpen: boolean;
  onChange(patch: Partial<ChannelInput>): void;
}) {
  const { tr } = useI18n();
  const [open, setOpen] = useState(defaultOpen);
  const [preview, setPreview] = useState<ChannelTemplatePreview | null>(null);
  const [previewErr, setPreviewErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<'preview' | 'starter' | null>(null);
  const markdownCapable = MARKDOWN_TYPES.has(type);
  const effectiveFormat: ChannelMessageFormat = markdownCapable ? format : '';
  const isWebhook = type === 'webhook';

  const update = (patch: Partial<ChannelInput>) => {
    setPreview(null);
    setPreviewErr(null);
    onChange(patch);
  };

  const runPreview = async () => {
    setBusy('preview');
    setPreviewErr(null);
    try {
      setPreview(await previewChannelTemplate({
        type,
        format: effectiveFormat,
        title_template: titleTemplate,
        content_template: contentTemplate,
      }));
    } catch (e) {
      setPreview(null);
      setPreviewErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const insertStarter = async () => {
    setBusy('starter');
    setPreviewErr(null);
    try {
      const starter = await getChannelTemplateStarter(type, effectiveFormat);
      update({ content_template: starter.content_template });
    } catch (e) {
      setPreviewErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="rounded-md border border-zinc-800 bg-zinc-950/40">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-controls="channel-template-editor"
        className="flex w-full items-center gap-1.5 px-3 py-2 text-left text-xs text-zinc-300"
      >
        {open ? <ChevronDown size={14} aria-hidden="true" /> : <ChevronRight size={14} aria-hidden="true" />}
        <span className="font-medium">{tr('消息模板（可选）', 'Message template (optional)')}</span>
        {!open && (titleTemplate || contentTemplate || effectiveFormat) && (
          <Chip tone="info">{tr('已自定义', 'Customized')}</Chip>
        )}
      </button>
      {open && (
        <div id="channel-template-editor" className="space-y-3 border-t border-zinc-800 px-3 pb-3 pt-2">
          <p className="text-[11px] text-zinc-500">
            {tr(
              '用 Go 模板语法自定义告警通知的标题和内容，只作用于告警通知；留空使用默认格式。模板渲染失败时会改发默认格式，并在告警事件里注明。',
              'Customize the title and body of alert notifications with Go template syntax. Only alert notifications use it; empty fields keep the default format. If a template fails at send time, the default format is sent and the alert timeline says so.',
            )}
          </p>
          {markdownCapable && (
            <Field label={tr('消息格式', 'Message format')}>
              <Select
                label={tr('消息格式', 'Message format')}
                value={effectiveFormat === 'markdown' ? 'markdown' : 'text'}
                onValueChange={(value) => update({ format: value === 'markdown' ? 'markdown' : '' })}
                className="w-full"
              >
                <option value="text">{tr('文本', 'Text')}</option>
                <option value="markdown">Markdown</option>
              </Select>
            </Field>
          )}
          <Field
            label={tr('标题模板', 'Title template')}
            hint={tr('单行。用于标题位（钉钉 / 飞书卡片 / Slack 标题）和默认格式的首行；留空沿用默认标题。', 'One line. Used where the platform shows a title (DingTalk, Feishu card, Slack) and as the first line of the default layout; empty keeps the default title.')}
          >
            <Input
              value={titleTemplate}
              onChange={(e) => update({ title_template: e.target.value })}
              placeholder="【{{upper .Severity}}】{{.RuleName}}"
              className="w-full font-mono"
            />
          </Field>
          <Field
            label={isWebhook ? tr('请求体模板（JSON）', 'Request body template (JSON)') : tr('内容模板', 'Content template')}
            hint={isWebhook
              ? tr('整个 POST 请求体，必须渲染成合法 JSON；字符串值请用 {{json .Title}} 转义。', 'The whole POST body; it must render to valid JSON. Quote string values with {{json .Title}}.')
              : tr('完整的消息正文。', 'The complete message body.')}
          >
            <Textarea
              value={contentTemplate}
              onChange={(e) => update({ content_template: e.target.value })}
              rows={8}
              spellCheck={false}
              placeholder={isWebhook
                ? tr('留空发送标准 JSON', 'Empty sends the standard JSON')
                : effectiveFormat === 'markdown'
                  ? tr('留空使用内置 Markdown 模板', 'Empty uses the built-in Markdown template')
                  : tr('留空使用默认格式', 'Empty keeps the default format')}
              className="w-full font-mono text-xs"
            />
          </Field>
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="outline" size="sm" onClick={runPreview} disabled={busy !== null}>
              {busy === 'preview' ? tr('预览中…', 'Rendering…') : tr('预览', 'Preview')}
            </Button>
            {!contentTemplate.trim() && (
              <Button variant="ghost" size="sm" onClick={insertStarter} disabled={busy !== null}>
                {busy === 'starter' ? tr('加载中…', 'Loading…') : tr('填入示例模板', 'Insert example template')}
              </Button>
            )}
          </div>
          {previewErr && (
            <div role="alert" className="rounded border border-red-500/40 bg-red-500/5 px-2 py-1 text-[11px] text-red-300">
              {previewErr}
            </div>
          )}
          {preview && (
            <div className="space-y-1.5 rounded border border-zinc-800 bg-zinc-950 px-3 py-2" aria-label={tr('模板预览', 'Template preview')}>
              <div className="text-[11px] text-zinc-500">
                {tr('示例告警渲染结果', 'Rendered with a sample alert')}
                {preview.default_layout && tr('（未设置内容模板，使用平台默认格式）', ' (no content template: the platform\'s default layout)')}
              </div>
              <div className="break-words text-xs font-medium text-zinc-100">{preview.title}</div>
              {preview.content && (
                <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words font-mono text-[11px] text-zinc-300">{preview.content}</pre>
              )}
            </div>
          )}
          <div className="space-y-1 text-[11px] text-zinc-500">
            <div>
              {tr('可用变量：', 'Variables: ')}
              <code className="break-words font-mono text-zinc-400">{TEMPLATE_VARIABLES.join('  ')}</code>
            </div>
            <div>
              {tr('函数：', 'Functions: ')}
              <code className="break-words font-mono text-zinc-400">{TEMPLATE_FUNCTIONS.join('  ')}</code>
              {tr('，例如 ', ', e.g. ')}
              <code className="font-mono text-zinc-400">{'{{date "2006-01-02 15:04:05" .OccurredAt}}'}</code>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <Label className="block">
      <span className="mb-1 block text-xs text-zinc-500">{label}</span>
      {children}
      {hint && <span className="mt-1 block text-[11px] text-zinc-500">{hint}</span>}
    </Label>
  );
}
