/**
 * 需求页：按项目浏览正式需求，并展示单个需求的真实进度与任务顺序。
 *
 * 约束：
 * - 需求与任务列表来自服务端（`GET /projects/{id}/requirements`、`GET /requirements/{id}`），
 *   进度来自 `GET /requirements/{id}/progress`；页面不推断完成度，不使用模型总结；
 * - 进度只按已完成任务数量计算，`failed` / `recovery_required` / `cancelled` / `paused`
 *   一律阻止"已完成"结论，并作为阻塞原因展示；
 * - 本页只读：不提供提前启动某个任务或调整任务顺序的入口（顺序由方案确认固定），
 *   也不提供需求删除入口（删除属于后续里程碑）；
 * - 任务通过 `?task_id=` 链接到任务页，用户在那里看到真实执行记录。
 */
import { ReloadOutlined } from "@ant-design/icons";
import { Alert, Descriptions, Flex, Select, Table, Tag, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { Button, useRouter } from "lingting-react-ui";
import { useCallback, useEffect, useMemo, useState } from "react";

import {
  fetchProjectRequirements,
  fetchRequirement,
  type ConfirmedRequirement,
  type ConfirmedTask,
  type Project,
  type RequirementProgressResponse,
} from "@/lib/api";
import { describeProjectExecutionError } from "@/pages/projects/useProjectExecution";
import { useProjects } from "@/pages/projects/useProjects";
import {
  describeBlockingReason,
  describeCounts,
  projectTaskStatusMeta,
  requirementProgressStatusMeta,
} from "@/pages/projects/projectExecution";
import { useRequirementProgress } from "@/pages/requirements/useRequirementProgress";

/** 从地址栏读取初始需求标识：项目执行面板用 `?requirement_id=` 深链接到本页 */
function readRequirementIDFromLocation(): string | undefined {
  if (typeof window === "undefined") return undefined;
  const value = new URLSearchParams(window.location.search).get("requirement_id");
  const trimmed = value?.trim();
  return trimmed ? trimmed : undefined;
}

/** 需求页 */
export function RequirementsPage() {
  const { navigate } = useRouter();
  const projects = useProjects();
  const [projectID, setProjectID] = useState<string>();
  const [requirements, setRequirements] = useState<ConfirmedRequirement[]>([]);
  const [listError, setListError] = useState<string>();
  const [listLoading, setListLoading] = useState(false);
  const [selectedID, setSelectedID] = useState<string | undefined>(readRequirementIDFromLocation);
  const [detail, setDetail] = useState<{
    requirement: ConfirmedRequirement;
    tasks: ConfirmedTask[];
  }>();
  const [detailError, setDetailError] = useState<string>();
  const [detailLoading, setDetailLoading] = useState(false);
  const progress = useRequirementProgress(selectedID);

  // 深链接只给了需求标识：从需求本身取出项目，随后再加载该项目的需求列表。
  const applyDetailProject = useCallback((projectIDValue?: string) => {
    if (!projectIDValue) return;
    setProjectID((current) => current ?? projectIDValue);
  }, []);

  const loadRequirements = useCallback(async (targetProjectID: string) => {
    setListLoading(true);
    setListError(undefined);
    try {
      const { data } = await fetchProjectRequirements(targetProjectID);
      setRequirements(data.records);
    } catch (cause) {
      setRequirements([]);
      setListError(describeProjectExecutionError(cause));
    } finally {
      setListLoading(false);
    }
  }, []);

  const loadDetail = useCallback(
    async (requirementID: string) => {
      setDetailLoading(true);
      setDetailError(undefined);
      try {
        const { data } = await fetchRequirement(requirementID);
        setDetail(data);
        applyDetailProject(data.requirement.project_id);
      } catch (cause) {
        setDetail(undefined);
        setDetailError(describeProjectExecutionError(cause));
      } finally {
        setDetailLoading(false);
      }
    },
    [applyDetailProject],
  );

  useEffect(() => {
    if (!projectID) return;
    void loadRequirements(projectID);
  }, [loadRequirements, projectID]);

  useEffect(() => {
    if (!selectedID) {
      setDetail(undefined);
      return;
    }
    void loadDetail(selectedID);
  }, [loadDetail, selectedID]);

  const requirementColumns = useMemo<ColumnsType<ConfirmedRequirement>>(
    () => [
      {
        dataIndex: "title",
        render: (_value, record) => (
          <Flex gap={0} vertical>
            <Typography.Text strong>{record.title}</Typography.Text>
            <Typography.Text type="secondary">{record.id}</Typography.Text>
          </Flex>
        ),
        title: "需求",
      },
      {
        dataIndex: "status",
        render: (_value, record) => <Tag>{record.status}</Tag>,
        title: "业务状态",
        width: 120,
      },
    ],
    [],
  );

  const taskColumns = useMemo<ColumnsType<ConfirmedTask>>(
    () => [
      {
        dataIndex: "requirement_sequence",
        render: (_value, record) => record.requirement_sequence ?? "—",
        title: "顺序",
        width: 72,
      },
      {
        dataIndex: "id",
        render: (_value, record) => <Typography.Text code>{record.id}</Typography.Text>,
        title: "任务",
      },
      {
        dataIndex: "status",
        render: (_value, record) => {
          const meta = projectTaskStatusMeta(record.status);
          return <Tag color={meta.color}>{meta.label}</Tag>;
        },
        title: "真实状态",
        width: 120,
      },
      {
        dataIndex: "goal",
        render: (_value, record) => (
          <Typography.Text ellipsis style={{ maxWidth: 320 }}>
            {record.goal || "—"}
          </Typography.Text>
        ),
        title: "目标",
      },
      {
        dataIndex: "option",
        render: (_value, record) => (
          <Button
            onClick={() => void navigate(`/tasks?task_id=${encodeURIComponent(record.id)}`)}
            text="查看任务"
            type="link"
          />
        ),
        title: "操作",
        width: 100,
      },
    ],
    [navigate],
  );

  return (
    <Flex gap="middle" vertical style={{ width: "100%" }}>
      <Alert
        description={
          "需求由方案确认生成，进度只按关联任务的真实状态计算：全部任务完成才是已完成，" +
          "存在失败、待核验、取消或暂停的任务时不会被判定为完成，也不会自动领取后续任务。" +
          "任务顺序在确认方案时固定，本页不提供调整顺序或提前启动后续任务的入口。"
        }
        showIcon
        title="需求与真实进度"
        type="info"
      />

      <Flex align="end" gap="small" wrap>
        <Flex gap={4} vertical>
          <Typography.Text type="secondary">项目</Typography.Text>
          <Select
            options={projects.projects.map((project: Project) => ({
              label: `${project.name}（${project.id}）`,
              value: project.id,
            }))}
            placeholder="选择项目后浏览该项目的需求"
            style={{ minWidth: 320 }}
            value={projectID}
            onChange={(value: string) => {
              setProjectID(value);
              setSelectedID(undefined);
            }}
          />
        </Flex>
        <Button
          icon={<ReloadOutlined />}
          onClick={() => {
            if (projectID) void loadRequirements(projectID);
            if (selectedID) {
              void loadDetail(selectedID);
              void progress.reload();
            }
          }}
          text="刷新"
        />
      </Flex>

      {projects.status === "error" && (
        <Alert description={projects.error} showIcon title="加载项目列表失败" type="error" />
      )}
      {listError && <Alert description={listError} showIcon title="加载需求列表失败" type="error" />}

      <Table<ConfirmedRequirement>
        columns={requirementColumns}
        dataSource={requirements}
        loading={listLoading}
        locale={{
          emptyText: projectID
            ? "该项目还没有正式需求：需求在方案确认后生成"
            : "请先选择项目",
        }}
        onRow={(record) => ({
          onClick: () => setSelectedID(record.id),
        })}
        pagination={false}
        rowClassName={(record) => (record.id === selectedID ? "ant-table-row-selected" : "")}
        rowKey="id"
        size="small"
      />

      {detailError && <Alert description={detailError} showIcon title="加载需求详情失败" type="error" />}
      {detailLoading && <Typography.Text type="secondary">正在加载需求详情…</Typography.Text>}

      {detail && (
        <Flex gap="middle" vertical>
          <Descriptions bordered column={2} size="small" title="需求详情">
            <Descriptions.Item label="标题" span={2}>
              {detail.requirement.title}
            </Descriptions.Item>
            <Descriptions.Item label="描述" span={2}>
              {detail.requirement.description || "—"}
            </Descriptions.Item>
            <Descriptions.Item label="需求标识">
              <Typography.Text code>{detail.requirement.id}</Typography.Text>
            </Descriptions.Item>
            <Descriptions.Item label="业务状态">
              <Tag>{detail.requirement.status}</Tag>
            </Descriptions.Item>
            <Descriptions.Item label="优先级">{detail.requirement.priority}</Descriptions.Item>
            <Descriptions.Item label="确认时间">
              {formatTime(detail.requirement.confirmed_at)}
            </Descriptions.Item>
          </Descriptions>

          {progress.status === "error" && (
            <Alert
              action={<Button onClick={() => void progress.reload()} size="small" text="重试" />}
              description={progress.error}
              showIcon
              title="加载需求进度失败"
              type="error"
            />
          )}
          {progress.status === "ready" && (
            <RequirementProgressPanel progress={progress.progress} />
          )}

          <Table<ConfirmedTask>
            columns={taskColumns}
            dataSource={detail.tasks}
            locale={{ emptyText: "该需求还没有正式任务" }}
            pagination={false}
            rowKey="id"
            size="small"
            title={() => <Typography.Text strong>任务（按需求内顺序排列）</Typography.Text>}
          />
        </Flex>
      )}
    </Flex>
  );
}

/** 需求进度面板：展示真实计数、阻塞原因与当前 / 下一个任务 */
function RequirementProgressPanel({ progress }: { progress: RequirementProgressResponse }) {
  const meta = requirementProgressStatusMeta(progress.status);
  return (
    <Flex gap="small" vertical>
      <Flex align="center" gap="small" wrap>
        <Tag color={meta.color}>{meta.label}</Tag>
        <Typography.Text>
          已完成 {progress.current} / 共 {progress.total} 个任务
        </Typography.Text>
        <Typography.Text type="secondary">{describeCounts(progress.counts)}</Typography.Text>
      </Flex>
      {progress.blocked && (
        <Alert
          description={
            <Flex gap={2} vertical>
              <Typography.Text>
                {describeBlockingReason(progress.blocked_reason) ?? progress.blocked_reason}
              </Typography.Text>
              {progress.blocked_task_id && (
                <Typography.Text code>{progress.blocked_task_id}</Typography.Text>
              )}
            </Flex>
          }
          showIcon
          title="该需求被阻塞，不会被判定为完成"
          type="warning"
        />
      )}
      <Flex gap="large" wrap>
        <Typography.Text type="secondary">
          当前任务：{progress.current_task_id ?? "无"}
        </Typography.Text>
        <Typography.Text type="secondary">下一个任务：{progress.next_task_id ?? "无"}</Typography.Text>
      </Flex>
    </Flex>
  );
}

/** 时间展示：使用本地时区，缺省显示占位符 */
function formatTime(value?: string): string {
  if (!value) return "—";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return "—";
  return parsed.toLocaleString();
}
