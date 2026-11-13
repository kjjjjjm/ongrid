import { act, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import SettingsNotifications from './Notifications';
import { server } from '@/test/msw-server';

const now = '2026-11-12T00:00:00Z';
const maskedEndpoint = 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?k...';

beforeEach(() => {
  localStorage.setItem('ongrid-locale', 'zh-CN');
});

function listWith(channel: Record<string, unknown>) {
  return http.get('/api/v1/notification-channels', () => HttpResponse.json({
    items: [{ id: 3, name: '运维群', type: 'wecom', enabled: true, endpoint: maskedEndpoint, created_at: now, updated_at: now, ...channel }],
    total: 1,
  }));
}

describe('notification channel templates', () => {
  it('marks templated channels in the list', async () => {
    server.use(listWith({ format: 'markdown' }));
    render(<MemoryRouter><SettingsNotifications /></MemoryRouter>);
    const card = await screen.findByTestId('notification-provider-wecom');
    expect(card).toHaveTextContent('自定义模板 · Markdown');
  });

  it('edits, previews and saves a Markdown template without touching the masked endpoint', async () => {
    let saved: Record<string, unknown> | null = null;
    let previewed: Record<string, unknown> | null = null;
    server.use(
      listWith({}),
      http.get('/api/v1/notification-channels/template-starter', ({ request }) => {
        const url = new URL(request.url);
        return HttpResponse.json({ content_template: `starter:${url.searchParams.get('type')}/${url.searchParams.get('format')}` });
      }),
      http.post('/api/v1/notification-channels/preview', async ({ request }) => {
        previewed = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ title: '【CRITICAL】主机 CPU 使用率过高', content: '**渲染结果** 93.5', format: 'markdown', default_layout: false });
      }),
      http.put('/api/v1/notification-channels/3', async ({ request }) => {
        saved = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ id: 3, name: '运维群', type: 'wecom', enabled: true, created_at: now, updated_at: now });
      }),
    );

    render(<MemoryRouter><SettingsNotifications /></MemoryRouter>);
    const card = await screen.findByTestId('notification-provider-wecom');
    const user = userEvent.setup();
    await act(async () => {
      await user.click(within(card).getByRole('button', { name: /编辑/ }));
    });
    const dialog = await screen.findByRole('dialog', { name: '编辑：运维群' });

    const toggle = within(dialog).getByRole('button', { name: /消息模板（可选）/ });
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    await act(async () => {
      await user.click(toggle);
    });

    await act(async () => {
      await user.click(within(dialog).getByRole('combobox', { name: '消息格式' }));
    });
    await act(async () => {
      await user.click(screen.getByRole('option', { name: 'Markdown' }));
    });

    await act(async () => {
      await user.click(within(dialog).getByRole('button', { name: '填入示例模板' }));
    });
    const content = within(dialog).getByPlaceholderText('留空使用内置 Markdown 模板');
    await vi.waitFor(() => expect(content).toHaveValue('starter:wecom/markdown'));

    await act(async () => {
      // paste: `type` reads "[" and "{" as key descriptors.
      await user.click(within(dialog).getByPlaceholderText('【{{upper .Severity}}】{{.RuleName}}'));
      await user.paste('[prod] {{.RuleName}}');
      await user.click(within(dialog).getByRole('button', { name: '预览' }));
    });
    const preview = await within(dialog).findByLabelText('模板预览');
    expect(preview).toHaveTextContent('【CRITICAL】主机 CPU 使用率过高');
    expect(preview).toHaveTextContent('**渲染结果** 93.5');
    expect(previewed).toMatchObject({ type: 'wecom', format: 'markdown', title_template: '[prod] {{.RuleName}}', content_template: 'starter:wecom/markdown' });

    await act(async () => {
      await user.click(within(dialog).getByRole('button', { name: '保存' }));
    });
    await vi.waitFor(() => expect(saved).not.toBeNull());
    expect(saved).toMatchObject({
      endpoint: maskedEndpoint,
      format: 'markdown',
      title_template: '[prod] {{.RuleName}}',
      content_template: 'starter:wecom/markdown',
    });
  });

  it('shows why a template is invalid', async () => {
    server.use(
      listWith({ content_template: '{{.Nope}}' }),
      http.post('/api/v1/notification-channels/preview', () => HttpResponse.json(
        { error: 'invalid: can\'t evaluate field Nope', code: 'invalid' },
        { status: 400 },
      )),
    );

    render(<MemoryRouter><SettingsNotifications /></MemoryRouter>);
    const card = await screen.findByTestId('notification-provider-wecom');
    const user = userEvent.setup();
    await act(async () => {
      await user.click(within(card).getByRole('button', { name: /编辑/ }));
    });
    const dialog = await screen.findByRole('dialog', { name: '编辑：运维群' });
    // A channel that already has a template opens the editor expanded.
    expect(within(dialog).getByRole('button', { name: /消息模板（可选）/ })).toHaveAttribute('aria-expanded', 'true');

    await act(async () => {
      await user.click(within(dialog).getByRole('button', { name: '预览' }));
    });
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Nope');
  });
});
