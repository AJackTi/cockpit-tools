import { confirm as confirmDialog } from "@tauri-apps/plugin-dialog";
import type { TFunction } from "i18next";
import {
  isCodexAgentIdentityAccount,
  isCodexApiKeyAccount,
  isCodexChatCompletionsApiKeyAccount,
  isCodexNewApiAccount,
  isCodexWebSessionAccount,
  type CodexAccount,
} from "../types/codex";

/**
 * 风险确认只针对「普通 OAuth 账号」：API Key、Agent Identity、仅查额度的 Web Session、
 * Chat Completions / DeepSeek 等其他类型账号加入 API 服务时不再提示。
 */
export function shouldConfirmApiServiceRiskForAccount(
  account?: CodexAccount | null,
): boolean {
  if (!account) return false;
  return (
    !isCodexApiKeyAccount(account) &&
    !isCodexAgentIdentityAccount(account) &&
    !isCodexWebSessionAccount(account) &&
    !isCodexChatCompletionsApiKeyAccount(account) &&
    !isCodexNewApiAccount(account)
  );
}

/** 批量导入预览里的账号类型（后端取值：OAuth / API Key / Agent Identity / Access Token / "-"）。 */
export function shouldConfirmApiServiceRiskForAccountType(
  accountType?: string | null,
): boolean {
  return (accountType ?? "").trim().toLowerCase() === "oauth";
}

export interface ApiServiceRiskPrompt {
  message: string;
  options: {
    title: string;
    okLabel: string;
    cancelLabel: string;
  };
}

export function buildApiServiceRiskPrompt(t: TFunction): ApiServiceRiskPrompt {
  return {
    message: t(
      "codex.apiService.accountRiskConfirmMessage",
      "目前 API 服务可能被官方判定为异常使用，账号可能因此被标记或降智；近期不建议使用。确认继续把账号加入 API 服务吗？",
    ),
    options: {
      title: t(
        "codex.apiService.accountRiskConfirmTitle",
        "继续把账号加入 API 服务？",
      ),
      okLabel: t("common.confirm", "确认"),
      cancelLabel: t("common.cancel", "取消"),
    },
  };
}

/**
 * 把账号加入 API 服务前的二次确认。
 *
 * API 服务会用账号凭据在本机发起上游请求，存在被官方判定为异常使用、导致账号被标记
 * 或降智的风险；这里在加入前明确提示，避免用户误操作后才察觉。
 */
export async function confirmApiServiceAccountAdd(
  t: TFunction,
): Promise<boolean> {
  const { message, options } = buildApiServiceRiskPrompt(t);
  return confirmDialog(message, options);
}
