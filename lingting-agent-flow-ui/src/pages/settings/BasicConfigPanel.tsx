/**
 * 设置中心「基础配置」面板。
 *
 * 职责：只读展示服务启动时实际生效的四项运行配置，不提供编辑、保存或重启入口。
 * 数据全部来自 `GET /api/v1/settings/config`：加载中、请求失败与成功是三种不同状态，
 * 失败时展示服务端原因，不用默认值或空字符串伪装成功。
 */
import { Alert, Descriptions, Flex, Typography } from "antd";
import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError, fetchSettingsConfig, type SettingsConfigResponse } from "@/lib/api";
import { Button } from "lingting-react-ui";

/** 基础配置查询状态：失败与"没有配置"是两件不同的事，不合并 */
type BasicConfigState =
  | { status: "loading" }
  | { status: "ready"; config: SettingsConfigResponse }
  | { status: "error"; error: string };

/** 把未知错误转成可读说明，保留服务端返回的真实原因 */
function describeSettingsError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.kind === "network") return "无法连接 AgentFlow 服务，请确认服务已启动。";
    if (error.kind === "timeout") return "读取基础配置超时，请稍后重试。";
    if (error.kind === "aborted") return "读取基础配置已取消。";
    return error.message;
  }
  return error instanceof Error ? error.message : "未知错误";
}

/** 文本取值：服务端没有给出内容时如实显示"未提供"，不伪装成有效配置 */
function displayText(value: string | undefined): string {
  const trimmed = value?.trim() ?? "";
  return trimmed === "" ? "未提供" : trimmed;
}

/** 并发上限：非正数说明服务端没有给出有效值，同样显示"未提供" */
function displayConcurrency(value: number | undefined): string {
  return typeof value === "number" && value > 0 ? String(value) : "未提供";
}

export function BasicConfigPanel() {
  const [state, setState] = useState<BasicConfigState>({ status: "loading" });
  const [attempt, setAttempt] = useState(0);
  /** 只接受最近一次请求的结果，避免重新读取后旧响应覆盖新状态 */
  const requestRef = useRef(0);

  const reload = useCallback(() => {
    setState({ status: "loading" });
    setAttempt((value) => value + 1);
  }, []);

  useEffect(() => {
    const requestID = requestRef.current + 1;
    requestRef.current = requestID;
    const controller = new AbortController();
    fetchSettingsConfig({ signal: controller.signal })
      .then(({ data }) => {
        if (controller.signal.aborted || requestRef.current !== requestID) return;
        setState({ config: data, status: "ready" });
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted || requestRef.current !== requestID) return;
        setState({ error: describeSettingsError(cause), status: "error" });
      });
    return () => controller.abort();
  }, [attempt]);

  return (
    <Flex gap="middle" vertical style={{ width: "100%" }}>
      <Alert
        showIcon
        description={
          "展示服务启动时实际生效的运行配置，其中监听地址已包含启动参数覆盖。修改配置需要" +
          "编辑配置文件并重启服务，这里不提供编辑、保存或重启入口。"
        }
        title="只读查看"
        type="info"
      />

      <Flex align="center" gap="small" wrap>
        <Button onClick={reload} text="重新读取" />
        <Typography.Text type="secondary">
          取值来自服务端启动时的配置快照，请求时不会重新读取配置文件。
        </Typography.Text>
      </Flex>

      {state.status === "loading" && <Alert showIcon title="正在读取基础配置…" type="info" />}

      {state.status === "error" && (
        <Alert
          action={<Button onClick={reload} size="small" text="重试" />}
          description={state.error}
          showIcon
          title="读取基础配置失败"
          type="error"
        />
      )}

      {state.status === "ready" && (
        <Descriptions bordered column={1} size="small">
          <Descriptions.Item label="数据库路径">
            <Typography.Text code>{displayText(state.config.database_path)}</Typography.Text>
          </Descriptions.Item>
          <Descriptions.Item label="服务监听地址">
            <Typography.Text code>{displayText(state.config.listen_address)}</Typography.Text>
          </Descriptions.Item>
          <Descriptions.Item label="最大并发任务数">
            {displayConcurrency(state.config.max_concurrent_tasks)}
          </Descriptions.Item>
          <Descriptions.Item label="Session 根目录">
            <Typography.Text code>{displayText(state.config.session_root)}</Typography.Text>
          </Descriptions.Item>
        </Descriptions>
      )}
    </Flex>
  );
}
