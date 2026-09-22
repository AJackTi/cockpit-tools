import assert from 'node:assert/strict';
import test from 'node:test';
import type { TFunction } from 'i18next';
import { buildApiServiceRiskPrompt } from './apiServiceRiskConfirm';
import {
  shouldConfirmApiServiceRiskForAccount,
  shouldConfirmApiServiceRiskForAccountType,
} from './apiServiceRiskConfirm';
import type { CodexAccount } from '../types/codex';

function accountFixture(overrides: Partial<CodexAccount> = {}): CodexAccount {
  return {
    id: 'account',
    email: 'user@example.com',
    auth_mode: 'chatgpt',
    account_id: 'workspace',
    plan_type: 'plus',
    created_at: 0,
    last_used: 0,
    tokens: { id_token: 'id', access_token: 'access', refresh_token: 'refresh' },
    ...overrides,
  } as CodexAccount;
}

test('risk confirmation only applies to ordinary OAuth accounts', () => {
  assert.equal(shouldConfirmApiServiceRiskForAccount(accountFixture()), true);
  assert.equal(
    shouldConfirmApiServiceRiskForAccount(accountFixture({ auth_mode: 'apikey' })),
    false,
  );
  assert.equal(
    shouldConfirmApiServiceRiskForAccount(
      accountFixture({
        auth_mode: 'apikey',
        api_wire_api: 'chat_completions',
      }),
    ),
    false,
  );
  assert.equal(
    shouldConfirmApiServiceRiskForAccount(
      accountFixture({
        auth_mode: 'apikey',
        api_provider_id: 'cockpit_api',
      }),
    ),
    false,
  );
  assert.equal(
    shouldConfirmApiServiceRiskForAccount(
      accountFixture({
        agent_identity: {
          agent_runtime_id: 'runtime',
          agent_private_key: 'key',
          account_id: 'workspace',
          chatgpt_user_id: 'user',
        },
      }),
    ),
    false,
  );
  assert.equal(
    shouldConfirmApiServiceRiskForAccount(
      accountFixture({ token_source_mode: 'chatgpt_web_session' }),
    ),
    false,
  );
  assert.equal(shouldConfirmApiServiceRiskForAccount(null), false);

  assert.equal(shouldConfirmApiServiceRiskForAccountType('OAuth'), true);
  assert.equal(shouldConfirmApiServiceRiskForAccountType(' oauth '), true);
  assert.equal(shouldConfirmApiServiceRiskForAccountType('API Key'), false);
  assert.equal(shouldConfirmApiServiceRiskForAccountType('Agent Identity'), false);
  assert.equal(shouldConfirmApiServiceRiskForAccountType('Access Token'), false);
  assert.equal(shouldConfirmApiServiceRiskForAccountType('-'), false);
  assert.equal(shouldConfirmApiServiceRiskForAccountType(undefined), false);
});

test('api service risk prompt reads localized keys and keeps confirm/cancel labels', () => {
  const requestedKeys: string[] = [];
  const t = ((key: string, fallback?: string) => {
    requestedKeys.push(key);
    return fallback ?? key;
  }) as unknown as TFunction;

  const prompt = buildApiServiceRiskPrompt(t);

  assert.ok(requestedKeys.includes('codex.apiService.accountRiskConfirmTitle'));
  assert.ok(requestedKeys.includes('codex.apiService.accountRiskConfirmMessage'));
  assert.deepEqual(requestedKeys.slice(-2), ['common.confirm', 'common.cancel']);
  assert.equal(prompt.options.okLabel, '确认');
  assert.equal(prompt.options.cancelLabel, '取消');
  assert.match(prompt.options.title, /API 服务/);
  assert.match(prompt.message, /API 服务/);
});
