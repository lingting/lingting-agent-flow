import { Alert, Descriptions, Flex, Space, Typography } from "antd";
import { DictTag, type DictItem } from "lingting-react-ui";

import type { Host, ValidationCheck } from "@/lib/api";
import {
  checkStatusMeta,
  CHECK_STATUS_META,
  VALIDATION_CHECK_TITLES,
  validationStatusMeta,
} from "@/pages/machines/status";

/** 单项检查状态字典，供 DictTag 使用 */
const CHECK_STATUS_DICT: DictItem<string>[] = Object.entries(CHECK_STATUS_META).map(
  ([value, meta]) => ({
    label: meta.label,
    tagColor: meta.color,
    value,
  }),
);

/** 总体状态字典 */
const VALIDATION_STATUS_DICT: DictItem<string>[] = [
  { label: "校验中", tagColor: "processing", value: "checking" },
  { label: "正常", tagColor: "success", value: "normal" },
  { label: "异常", tagColor: "error", value: "abnormal" },
];

/** 时间展示：使用本地时区，缺省显示占位符 */
function formatTime(value?: string): string {
  if (!value) return "—";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return "—";
  return parsed.toLocaleString();
}

/** 把毫秒耗时格式化为可读文本 */
function formatDuration(millis: number): string {
  if (!Number.isFinite(millis) || millis <= 0) return "—";
  if (millis < 1000) return `${millis} ms`;
  return `${(millis / 1000).toFixed(2)} s`;
}

/** 单项检查卡片：展示状态、耗时、说明与镜像逐条结果 */
function CheckBlock({ check }: { check: ValidationCheck }) {
  const meta = checkStatusMeta(check.status);
  const title = VALIDATION_CHECK_TITLES[check.name] ?? check.title ?? check.name;
  return (
    <Flex gap={4} vertical>
      <Space align="center" size={8}>
        <Typography.Text strong>{title}</Typography.Text>
        <DictTag dict={CHECK_STATUS_DICT} value={check.status} />
        <Typography.Text type="secondary">
          耗时 {formatDuration(check.duration_millis)}
        </Typography.Text>
      </Space>
      {check.message && (
        <Typography.Text type={meta.color === "error" ? "danger" : undefined}>
          {check.message}
        </Typography.Text>
      )}
      {check.error_code && (
        <Typography.Text code type="secondary">
          {check.error_code}
        </Typography.Text>
      )}
      {check.details && check.details.length > 0 && (
        <Flex gap={2} vertical>
          {check.details.map((detail) => (
            <Typography.Text key={detail} type="secondary">
              · {detail}
            </Typography.Text>
          ))}
        </Flex>
      )}
      {check.images && check.images.length > 0 && (
        <Flex gap={2} vertical>
          {check.images.map((image) => (
            <Space align="start" key={image.resolved ?? image.reference} size={8}>
              <DictTag dict={CHECK_STATUS_DICT} value={image.status} />
              <Typography.Text code>{image.reference}</Typography.Text>
              <Typography.Text type="secondary">
                查询引用 {image.resolved ?? image.reference}，耗时{" "}
                {formatDuration(image.duration_millis)}
                {image.digest ? `，摘要 ${image.digest}` : ""}
                {image.platforms && image.platforms.length > 0
                  ? `，平台 ${image.platforms.join("、")}`
                  : ""}
                {image.message ? `，${image.message}` : ""}
              </Typography.Text>
            </Space>
          ))}
        </Flex>
      )}
    </Flex>
  );
}

/**
 * 执行节点校验详情面板。
 *
 * 只展示接口真实返回的字段：总体状态、摘要、三项检查结果、镜像逐条结果与能力边界说明。
 * 详情缺失时给出明确提示，不把缺失当成通过。
 */
export function HostValidationPanel({ host }: { host: Host }) {
  const meta = validationStatusMeta(host.validation_status);
  const details = host.validation_details;
  return (
    <Flex gap="small" vertical>
      <Space align="center" size={8}>
        <Typography.Text strong>环境预检</Typography.Text>
        <DictTag dict={VALIDATION_STATUS_DICT} value={host.validation_status} />
        <Typography.Text type="secondary">
          状态 {meta.label}，代次 {host.validation_generation}
        </Typography.Text>
        {!host.usable_for_remote_task && (
          <Typography.Text type="warning">当前不可用于远程任务执行</Typography.Text>
        )}
      </Space>

      {host.validation_message && (
        <Typography.Text type={meta.color === "error" ? "danger" : undefined}>
          {host.validation_message}
        </Typography.Text>
      )}

      <Descriptions column={2} size="small">
        <Descriptions.Item label="开始时间">
          {formatTime(host.validation_started_at)}
        </Descriptions.Item>
        <Descriptions.Item label="完成时间">
          {formatTime(host.validation_completed_at)}
        </Descriptions.Item>
      </Descriptions>

      {details ? (
        <>
          {details.checks.map((check) => (
            <CheckBlock check={check} key={check.name} />
          ))}
          {details.capabilities.length > 0 && (
            <Alert
              description={
                <Flex gap={2} vertical>
                  {details.capabilities.map((capability) => (
                    <Typography.Text key={capability} type="secondary">
                      {capability}
                    </Typography.Text>
                  ))}
                </Flex>
              }
              showIcon
              title="校验能力边界"
              type="info"
            />
          )}
        </>
      ) : (
        <Alert
          description="当前没有可用的校验详情。可能尚未完成校验，或详情无法解析；请重新校验后再查看。"
          showIcon
          title="校验详情不可用"
          type="warning"
        />
      )}
    </Flex>
  );
}
