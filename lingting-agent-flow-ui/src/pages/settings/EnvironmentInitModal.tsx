/**
 * 环境工作空间初始化对话框。
 *
 * 复用服务端既有初始化流程（`POST /api/v1/environments/init`）：预检通过后才创建远程
 * 目录、默认基础环境版本与模板语言环境。它不删除、不重置任何既有版本，因此除非用户
 * 显式点击，前端不会自动触发。
 *
 * 新建语言环境定义也走这条路径：语言环境模板（Dockerfile 与定义清单）只能由服务端
 * 生成，前端不能拼装 Dockerfile 替代模板。
 */
import { Alert, Flex, Input, Modal, Typography } from "antd";
import { useState } from "react";

import { initEnvironment, type EnvironmentInitResponse, type Host } from "@/lib/api";
import {
  describeEnvironmentError,
  environmentWriteBlockedReason,
  parseLanguageNames,
} from "@/pages/settings/settings-environments";

export type EnvironmentInitModalProps = {
  /** 目标执行节点；为空时不打开对话框 */
  host?: Host;
  onCancel: () => void;
  /** 初始化成功（或部分成功）后回调，调用方据此重新读取真实环境数据 */
  onFinished: (result: EnvironmentInitResponse) => void;
  open: boolean;
};

export function EnvironmentInitModal({
  host,
  onCancel,
  onFinished,
  open,
}: EnvironmentInitModalProps) {
  const [names, setNames] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string>();
  const [result, setResult] = useState<EnvironmentInitResponse>();

  const blockedReason = environmentWriteBlockedReason(host);

  const handleSubmit = async () => {
    if (!host) return;
    setSubmitting(true);
    setError(undefined);
    try {
      const { data } = await initEnvironment({
        host_id: host.id,
        with_languages: parseLanguageNames(names),
      });
      setResult(data);
      onFinished(data);
    } catch (cause) {
      // 初始化失败必须展示服务端原因：可能是预检未通过、目录不可写或版本已存在。
      setError(describeEnvironmentError(cause));
    } finally {
      setSubmitting(false);
    }
  };

  const handleClose = () => {
    setNames("");
    setError(undefined);
    setResult(undefined);
    onCancel();
  };

  return (
    <Modal
      destroyOnHidden
      okButtonProps={{
        disabled: Boolean(blockedReason),
        loading: submitting,
      }}
      okText={result ? "完成" : "开始初始化"}
      open={open}
      title="初始化环境工作空间"
      onCancel={handleClose}
      onOk={() => {
        if (result) {
          handleClose();
          return;
        }
        void handleSubmit();
      }}
    >
      <Flex gap="middle" vertical>
        {blockedReason && <Alert showIcon description={blockedReason} type="warning" />}
        <Alert
          showIcon
          description={
            "服务端会先做远程 Docker 预检（SSH、CLI、Engine、API 兼容性、权限、工作空间可写），" +
            "预检不通过时不创建任何目录。已存在的环境版本不会被覆盖。"
          }
          title="初始化范围"
          type="info"
        />
        <Flex gap={4} vertical>
          <Typography.Text>执行节点</Typography.Text>
          <Typography.Text type="secondary">
            {host ? `${host.name}（${host.host}:${host.port}）` : "尚未选择执行节点"}
          </Typography.Text>
        </Flex>
        <Flex gap={4} vertical>
          <Typography.Text>同时创建的语言环境（可选）</Typography.Text>
          <Input
            aria-label="语言环境名称"
            disabled={Boolean(blockedReason) || submitting}
            placeholder="例如 go, node，用逗号分隔"
            value={names}
            onChange={(event) => setNames(event.target.value)}
          />
          <Typography.Text type="secondary">
            语言环境按服务端内置模板创建，模板之外的内容请通过「编辑 Dockerfile 生成新版本」提交。
          </Typography.Text>
        </Flex>
        {error && <Alert showIcon description={error} title="初始化失败" type="error" />}
        {result && (
          <Alert
            showIcon
            description={
              <Flex gap={4} vertical>
                <Typography.Text>{`远程工作空间: ${result.workspace_root}`}</Typography.Text>
                <Typography.Text>
                  {`预检结果: ${result.preflight.ready ? "通过" : "未通过"}`}
                </Typography.Text>
                <Typography.Text>
                  {`新建路径数: ${result.created.length}；环境: ${
                    result.environments.join("、") || "无"
                  }`}
                </Typography.Text>
              </Flex>
            }
            title="初始化已完成"
            type="success"
          />
        )}
      </Flex>
    </Modal>
  );
}
