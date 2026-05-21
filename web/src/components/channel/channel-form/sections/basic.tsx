"use client";

import { useEffect, useRef, useState } from "react";
import { useTranslations } from "next-intl";
import { Loader2, LogIn } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { FieldTip } from "@/components/business/field-tip";
import { StatusSelect } from "@/components/business/status-select";
import { ChannelForm } from "../types";
import { parseOtherSettings, stringifyOtherSettings } from "../utils";
import { API_TYPES, CHANNEL_TYPES } from "@/lib/constants";
import { usePollCopilotDeviceLogin, useStartCopilotDeviceLogin } from "@/lib/api/channels";
import { toast } from "sonner";

function formatTypeName(
  name: string,
  i18nKey: string,
  t: ReturnType<typeof useTranslations<"channels">>
): string {
  if (i18nKey) {
    try {
      return t(i18nKey as never);
    } catch {
      // Fall back to backend-provided canonical name when i18n key is missing.
    }
  }
  return name;
}

export interface BasicSectionProps {
  form: ChannelForm;
  setForm: (next: ChannelForm) => void;
  channelTypes: { id: number; name: string; i18n_key: string }[];
  showStatus?: boolean;
}

export function BasicSection({
  form,
  setForm,
  channelTypes,
  showStatus = false,
}: BasicSectionProps) {
  const t = useTranslations("channels");
  const tc = useTranslations("common");

  const channelType = Number(form.type);
  const typeOptions = [...channelTypes];
  if (
    Number.isFinite(channelType) &&
    channelType > 0 &&
    !typeOptions.some((item) => item.id === channelType)
  ) {
    typeOptions.push({ id: channelType, name: "Unknown", i18n_key: "" });
  }
  typeOptions.sort((a, b) => a.id - b.id);

  return (
    <div className="space-y-4">
      {/* Legacy Mode Toggle */}
      <div
        className={
          form.use_legacy_adaptor
            ? "flex items-center justify-between rounded-md border border-yellow-500/30 bg-yellow-500/5 px-4 py-3"
            : "flex items-center justify-between rounded-md border px-4 py-3"
        }
      >
        <div className="space-y-0.5">
          <Label>{t("useLegacyAdaptor")}</Label>
          <p className="text-sm text-muted-foreground">{t("useLegacyAdaptorTip")}</p>
        </div>
        <Switch
          checked={form.use_legacy_adaptor}
          onCheckedChange={(v) => setForm({ ...form, use_legacy_adaptor: v })}
        />
      </div>

      <div className="space-y-2">
        <Label>{t("type")}</Label>
        <Select value={form.type} onValueChange={(v) => setForm({ ...form, type: v })}>
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {typeOptions.map((item) => (
              <SelectItem key={item.id} value={String(item.id)}>
                {`${formatTypeName(item.name, item.i18n_key, t)} [${item.id}]`}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {/* Name */}
      <div className="space-y-2">
        <Label>{tc("name")}</Label>
        <Input
          value={form.name}
          onChange={(e) => setForm({ ...form, name: e.target.value })}
        />
      </div>

      {/* API Key */}
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <Label>{t("apiKey")}</Label>
          {channelType === CHANNEL_TYPES.GITHUB_COPILOT && (
            <CopilotLoginButton form={form} setForm={setForm} />
          )}
        </div>
        <Input value={form.key} onChange={(e) => setForm({ ...form, key: e.target.value })} />
      </div>

      {/* Base URL */}
      <div className="space-y-2">
        <Label>{t("baseUrl")}</Label>
        <Input
          value={form.base_url}
          onChange={(e) => setForm({ ...form, base_url: e.target.value })}
        />
      </div>

      {/* Status (edit mode only) */}
      {showStatus && (
        <StatusSelect
          value={form.status}
          onChange={(v) => setForm({ ...form, status: v })}
        />
      )}

      {/* Auto Ban */}
      <div className="flex items-center justify-between">
        <Label>
          {t("autoBan")}
          <FieldTip text={t("autoBanTip")} />
        </Label>
        <Switch
          checked={form.auto_ban === "1"}
          onCheckedChange={(v) => setForm({ ...form, auto_ban: v ? "1" : "0" })}
        />
      </div>

      {/* Tag & Remark */}
      <div className="grid grid-cols-2 gap-4">
        <div className="space-y-2">
          <Label>
            {t("tag")}
            <FieldTip text={t("tagTip")} />
          </Label>
          <Input
            value={form.tag}
            onChange={(e) => setForm({ ...form, tag: e.target.value })}
          />
        </div>
        <div className="space-y-2">
          <Label>
            {t("remark")}
            <FieldTip text={t("remarkTip")} />
          </Label>
          <Input
            value={form.remark}
            onChange={(e) => setForm({ ...form, remark: e.target.value })}
          />
        </div>
      </div>
    </div>
  );
}

function CopilotLoginButton({
  form,
  setForm,
}: {
  form: ChannelForm;
  setForm: (next: ChannelForm) => void;
}) {
  const t = useTranslations("channels");
  const tc = useTranslations("common");
  const startLogin = useStartCopilotDeviceLogin();
  const pollLogin = usePollCopilotDeviceLogin();
  const [open, setOpen] = useState(false);
  const [enterpriseUrl, setEnterpriseUrl] = useState("");
  const [device, setDevice] = useState<{
    verification_uri: string;
    user_code: string;
    device_code: string;
    interval: number;
    base_url: string;
    enterprise_url?: string;
  } | null>(null);
  const pollingRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (pollingRef.current) clearTimeout(pollingRef.current);
    };
  }, []);

  const applyCopilotDefaults = (token: string, baseUrl: string, enterpriseDomain?: string) => {
    const otherSettings = parseOtherSettings(form.other_settings);
    if (enterpriseDomain) {
      otherSettings.copilot_enterprise_domain = enterpriseDomain;
    }
    setForm({
      ...form,
      key: token,
      base_url: baseUrl,
      supported_api_types: JSON.stringify([
        API_TYPES.CHAT_COMPLETION,
        API_TYPES.RESPONSES,
      ]),
      endpoints: JSON.stringify({
        chat_completions: "/chat/completions",
        responses: "/responses",
        models: "/models",
      }),
      other_settings: stringifyOtherSettings(otherSettings),
      use_legacy_adaptor: false,
    });
  };

  const schedulePoll = (nextDevice: NonNullable<typeof device>, delaySeconds: number) => {
    if (pollingRef.current) clearTimeout(pollingRef.current);
    pollingRef.current = setTimeout(async () => {
      try {
        const result = await pollLogin.mutateAsync({
          device_code: nextDevice.device_code,
          enterprise_url: nextDevice.enterprise_url,
        });
        if (result.status === "success" && result.access_token) {
          applyCopilotDefaults(result.access_token, nextDevice.base_url, nextDevice.enterprise_url);
          toast.success(t("copilotLoginSuccess"));
          setOpen(false);
          setDevice(null);
          return;
        }
        if (result.status === "failed") {
          toast.error(result.error || tc("error"));
          return;
        }
        schedulePoll(nextDevice, result.interval || delaySeconds + 5);
      } catch (error) {
        toast.error(error instanceof Error ? error.message : tc("error"));
      }
    }, Math.max(delaySeconds, 1) * 1000 + 3000);
  };

  const start = async () => {
    try {
      const result = await startLogin.mutateAsync({ enterprise_url: enterpriseUrl.trim() || undefined });
      setDevice(result);
      window.open(result.verification_uri, "_blank", "noopener,noreferrer");
      schedulePoll(result, result.interval);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : tc("error"));
    }
  };

  return (
    <>
      <Button type="button" variant="outline" size="sm" onClick={() => setOpen(true)}>
        <LogIn className="mr-2 size-4" />
        {t("copilotLogin")}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("copilotLogin")}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4">
            {!device && (
              <>
                <div className="space-y-2">
                  <Label>{t("copilotEnterpriseUrl")}</Label>
                  <Input
                    value={enterpriseUrl}
                    placeholder="company.ghe.com"
                    onChange={(e) => setEnterpriseUrl(e.target.value)}
                  />
                </div>
                <Button type="button" onClick={start} disabled={startLogin.isPending}>
                  {startLogin.isPending && <Loader2 className="mr-2 size-4 animate-spin" />}
                  {t("copilotStartLogin")}
                </Button>
              </>
            )}
            {device && (
              <div className="space-y-3">
                <div className="rounded-md border p-3">
                  <p className="text-sm text-muted-foreground">{device.verification_uri}</p>
                  <p className="mt-2 font-mono text-2xl font-semibold tracking-widest">{device.user_code}</p>
                </div>
                <div className="flex items-center gap-2 text-sm text-muted-foreground">
                  <Loader2 className="size-4 animate-spin" />
                  <span>{t("copilotWaiting")}</span>
                </div>
              </div>
            )}
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}
