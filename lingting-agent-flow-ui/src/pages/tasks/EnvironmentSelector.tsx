/**
 * 任务创建表单的环境与版本选择器。
 *
 * 选项全部来自服务端真实数据：环境名称不再手工填写，版本只能从该环境的版本列表中
 * 选择，且只有 `usable=true` 的版本可选。不可用版本仍然展示构建状态与诊断摘要，
 * 但被禁用——隐藏它们会让用户无法判断"为什么没有可用版本"。
 */
import { Alert, Flex, Select, Typography } from "antd";

import type { Environment } from "@/lib/api";
import {
  environmentSelectOptions,
  findTaskEnvironment,
  taskEnvironments,
  usableVersions,
  versionSelectOptions,
} from "@/pages/tasks/environments";
import type { EnvironmentQueryState } from "@/pages/tasks/useEnvironments";

export type EnvironmentSelectorProps = {
  /** 当前环境查询状态 */
  query: EnvironmentQueryState;
  /** 当前选中的环境名称 */
  environment?: string;
  /** 当前选中的版本标签 */
  version?: string;
  /** 当前表单中的执行节点标识，用于解释"为什么没有发起查询" */
  hostID?: string;
  /** 环境变化时回调；版本由调用方按新环境重新计算 */
  onEnvironmentChange: (environment?: Environment) => void;
  onVersionChange: (version?: string) => void;
};

/** 展示环境查询的加载、失败与空列表状态，不把失败伪装成空列表 */
function QueryNotice({ hostID, query }: { hostID?: string; query: EnvironmentQueryState }) {
  if (!hostID?.trim()) {
    return (
      <Alert
        showIcon
        description="填写执行节点标识后会自动查询该节点上真实存在的环境与构建状态。"
        style={{ marginBottom: 16 }}
        title="尚未填写执行节点"
        type="info"
      />
    );
  }
  if (query.status === "loading") {
    return (
      <Alert
        showIcon
        style={{ marginBottom: 16 }}
        title="正在查询该执行节点上的环境…"
        type="info"
      />
    );
  }
  if (query.status === "error") {
    return (
      <Alert
        showIcon
        description={query.error}
        style={{ marginBottom: 16 }}
        title="环境查询失败"
        type="error"
      />
    );
  }
  if (query.status === "ready" && query.environments.length > 0) {
    const languages = taskEnvironments(query.environments);
    if (languages.length === 0) {
      return (
        <Alert
          showIcon
          description="该执行节点上只有基础环境，没有可用于任务的语言环境。"
          style={{ marginBottom: 16 }}
          title="没有可用的语言环境"
          type="warning"
        />
      );
    }
    if (!languages.some((environment) => usableVersions(environment).length > 0)) {
      return (
        <Alert
          showIcon
          description="该执行节点上的语言环境都没有可用版本（构建中或构建失败），暂不可用于任务启动。"
          style={{ marginBottom: 16 }}
          title="没有可用的环境版本"
          type="warning"
        />
      );
    }
  }
  if (query.status === "ready" && query.environments.length === 0) {
    return (
      <Alert
        showIcon
        description="该执行节点上还没有任何环境版本，请先通过 agentflow docker init 与 docker env build 生成可用版本。"
        style={{ marginBottom: 16 }}
        title="没有可用的环境"
        type="warning"
      />
    );
  }
  return null;
}

/** 展示所选环境没有可用版本的原因，禁止静默选择构建中或构建失败的版本 */
function SelectionNotice({ environment }: { environment?: Environment }) {
  if (!environment) return null;
  if (environment.versions.some((version) => version.usable)) return null;
  return (
    <Alert
      showIcon
      description={`环境 ${environment.name} 的版本均不可用（最新创建版本 ${
        environment.latest_version
      }），该环境暂不可用于任务启动。`}
      style={{ marginBottom: 16 }}
      title="该环境暂不可用"
      type="warning"
    />
  );
}

export function EnvironmentSelector({
  environment,
  hostID,
  onEnvironmentChange,
  onVersionChange,
  query,
  version,
}: EnvironmentSelectorProps) {
  const selected = findTaskEnvironment(query.environments, environment);
  const environmentOptions = environmentSelectOptions(query.environments).map((option) => ({
    disabled: option.disabled,
    label: option.label,
    title: option.unavailableReason,
    value: option.value,
  }));
  const versionOptions = versionSelectOptions(selected).map((option) => ({
    disabled: option.disabled,
    label: option.label,
    title: option.errorSummary,
    value: option.value,
  }));

  return (
    <Flex gap="small" vertical>
      <QueryNotice hostID={hostID} query={query} />
      <Flex gap="middle" wrap>
        <Flex gap={4} style={{ flex: 1, minWidth: 220 }} vertical>
          <Typography.Text>语言环境</Typography.Text>
          <Select
            allowClear
            aria-label="语言环境"
            disabled={query.status !== "ready"}
            options={environmentOptions}
            placeholder={query.status === "ready" ? "选择真实环境" : "等待环境查询完成后可选"}
            value={environment}
            onChange={(value) => {
              const next = findTaskEnvironment(query.environments, value);
              onEnvironmentChange(next);
            }}
          />
        </Flex>
        <Flex gap={4} style={{ flex: 1, minWidth: 220 }} vertical>
          <Typography.Text>环境版本</Typography.Text>
          <Select
            allowClear
            aria-label="环境版本"
            disabled={!selected}
            options={versionOptions}
            placeholder={selected ? "选择可用版本" : "请先选择环境"}
            value={version}
            onChange={(value) => onVersionChange(value)}
          />
        </Flex>
      </Flex>
      <SelectionNotice environment={selected} />
      {selected?.current_version && (
        <Typography.Text type="secondary">
          {`服务端当前可用版本：${selected.current_version}；最新创建版本：${selected.latest_version}`}
        </Typography.Text>
      )}
    </Flex>
  );
}
