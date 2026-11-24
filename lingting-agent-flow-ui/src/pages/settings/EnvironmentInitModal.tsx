/**
 * 环境工作空间初始化对话框。
 *
 * 复用服务端既有初始化流程（`POST /api/v1/environments/init`）：预检通过后才创建远程
 * 目录、默认基础环境版本与模板语言环境。它不删除、不重置任何既有版本，因此除非用户
 * 显式点击，前端不会自动触发。
 *
 * 可同时创建的语言环境只能来自服务端内置模板：语言环境模板（Dockerfile 与定义清单）
 * 由服务端生成，前端只提交模板名称，不拼装 Dockerfile 替代模板。
 */
import { Flex, Modal, Select, Typography, message } from "antd";
import { useState } from "react";

import { initEnvironment, type EnvironmentInitResponse, type Host } from "@/lib/api";
import {
  LANGUAGE_TEMPLATE_OPTIONS,
  describeEnvironmentError,
  environmentWriteBlockedReason,
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
  const [names, setNames] = useState<string[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const [result, setResult] = useState<EnvironmentInitResponse>();

  const blockedReason = environmentWriteBlockedReason(host);

  const handleSubmit = async () => {
    if (!host) return;
    setSubmitting(true);
    try {
      const { data } = await initEnvironment({
        host_id: host.id,
        with_languages: names,
      });
      setResult(data);
      onFinished(data);
    } catch (cause) {
      // 初始化失败必须展示服务端原因：可能是预检未通过、目录不可写或版本已存在。
      message.error(`初始化失败：${describeEnvironmentError(cause)}`);
    } finally {
      setSubmitting(false);
    }
  };

  const handleClose = () => {
    setNames([]);
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
        {blockedReason && <Typography.Text type="warning">{blockedReason}</Typography.Text>}
        <Typography.Text type="secondary">
          服务端会先做远程 Docker 预检（SSH、CLI、Engine、API 兼容性、权限、工作空间可写），
          预检不通过时不创建任何目录。已存在的环境版本不会被覆盖。
        </Typography.Text>
        <Flex gap={4} vertical>
          <Typography.Text>执行节点</Typography.Text>
          <Typography.Text type="secondary">
            {host ? `${host.name}（${host.host}:${host.port}）` : "尚未选择执行节点"}
          </Typography.Text>
        </Flex>
        <Flex gap={4} vertical>
          <Typography.Text>同时创建的语言环境（可选）</Typography.Text>
          <Select
            allowClear
            aria-label="语言环境模板"
            disabled={Boolean(blockedReason) || submitting}
            mode="multiple"
            options={LANGUAGE_TEMPLATE_OPTIONS}
            placeholder="从内置模板中选择，可多选"
            value={names}
            onChange={setNames}
          />
          <Typography.Text type="secondary">
            语言环境按服务端内置模板创建，模板之外的内容请通过「编辑 Dockerfile 生成新版本」提交。
          </Typography.Text>
        </Flex>
        {result && (
          <Flex gap={4} vertical>
            <Typography.Text strong type="success">
              初始化已完成
            </Typography.Text>
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
        )}
      </Flex>
    </Modal>
  );
}
