# Streaming json parser testing ground

## Problem

This proof of concept explores efficiently handling AWS Config history files, which can be large (GB), 
containing many ConfigItem entries. 

The target use case is a system that
* reads a Config history json file 
* iteratively splits config items from an array
* can enrich the outgoing message
* passes the items downstream for farther processing

The target language is go v1.17+

## Requirements

One target scenario is processing items contained in an AWS Config Snapshot source files residing in an S3 bucket.
The file sizes vary per account and time but can be 15GB+ in size.

The source files primary payload is an array of config items. In the case of the Config Snapshot, 
those are contained in field `configurationItems`.

The source file has top-level fields, some of which, like `configSnapshotId`, we wish to copy to the items emitted.

We use that concrete scenario to prototype a solution while keeping in mind this will be a recurring requirement for 
similar data structures.

## Solutions Considered

**Read the file in toto, then unmarshal into a struct**

This is suitable for small payloads, but is unsuitable for this scenario 
as it requires more disk space and memory than desired, or possibly available. 

This option requires waiting for the entire file to be read and loaded into memory
before item processing can begin.
If possible, treating the input as a stream is preferable.

This is not a new problem, and there are bound to be better solutions.

**Use streaming support from the go json package** 

Benefits
* relatively simple, small, fast, flexible
* can be deployed to multiple targets: e.g. lambda function, ECS task, k8s

**distributed streaming or data platform**

A distributed data processing system could certainly apply, but we're not considering that
initially. 

We hope to be able to accomplish the goals using a go program deployed as a lambda function. 
or as a container image in a lambda function, or a microservices container management system such as Amazon ECS or k8s 

## Proposed Solution

Use the streaming json support in go stdlib to implement a solution

### Additional design goals and features

Parse objects from the array into discrete items to send downstream for processing. 
Using this pattern separates the parsing, output, and processing concerns into discrete, scalable implementations.

Capture any global information from the notification message into each item. 
e.g. Add the parent snapshotId to each item for grouping and data lineage purposes.

TODO 

Instrument the application
* e.g. use send messages upon processing start and end to monitoring system. 
The end message can summarize what was processed, number of items, any errors, duration, etc.
* metrics
* logs
* traces

#### Program Internals

The program decodes the json input as a stream, 
emitting enriched items from the configurationItems array as they are read.

The decoder signals on a channel with the item data. 
A configurable number of workers in a worker pool receive the signals with data and write the item data.

Emitting new items to a channel as they are parsed from the stream and enriched
allows the decoder to dispatch enriched items in parallel to a pool of writer functions 
as the items are being read and decoded. 
The writer pool enables more efficient output as i/o delays are spread across a number of workers.

The json stream decoder knows nothing about the item source or destination; separating input, decode, and output concerns, 
thereby decoupling the components.

## Results

Running tests on a late 2015 iMac so they are only an indication of relative performance varying writer pool sizes.
My test machine has a 4 core i7 processor.

Example file content, showing one entry in configurationItems.
```json
{
  "fileVersion": "1.0",
  "configSnapshotId": "0f1d63cc-aee4-48b8-82ab-4f38087be14e",
  "configurationItems": [
    {
      "availabilityZone": "Not Applicable",
      "awsAccountId": "123456789012",
      "awsRegion": "us-east-1",
      "configuration": {
        "blockPublicAcls": true,
        "blockPublicPolicy": true,
        "ignorePublicAcls": true,
        "restrictPublicBuckets": true
      },
      "configurationItemCaptureTime": "2022-08-01T21:59:26.276Z",
      "configurationItemStatus": "ResourceDiscovered",
      "configurationItemVersion": "1.3",
      "configurationStateId": 1659391166276,
      "configurationStateMd5Hash": "",
      "fileVersion": "1.0",
      "relatedEvents": [],
      "relationships": [],
      "resourceId": "123456789012",
      "resourceType": "AWS::S3::AccountPublicAccessBlock",
      "supplementaryConfiguration": {},
      "tags": {}
    }
  ]
}
```

Output items have the parent `configSnapshotId` and `fileVersion` values added to each configurationItem.
This sample has the enrichment data both in the main body and a new field `metadata` for illustration.

```json
{
  "ARN": "arn:aws:codedeploy:us-east-1:123456789012:deploymentconfig:CodeDeployDefault.ECSLinear10PercentEvery1Minutes",
  "availabilityZone": "Not Applicable",
  "awsAccountId": "123456789012",
  "awsRegion": "us-east-1",
  "configSnapshotId": "0f1d63cc-aee4-48b8-82ab-4f38087be14e",
  "configuration": {
    "computePlatform": "ECS",
    "deploymentConfigId": "00000000-0000-0000-0000-000000000015",
    "deploymentConfigName": "CodeDeployDefault.ECSLinear10PercentEvery1Minutes",
    "trafficRoutingConfig": {
      "timeBasedLinear": {
        "linearInterval": 1,
        "linearPercentage": 10
      },
      "type": "TimeBasedLinear"
    }
  },
  "configurationItemCaptureTime": "2022-08-01T21:59:27.527Z",
  "configurationItemStatus": "ResourceDiscovered",
  "configurationItemVersion": "1.3",
  "configurationStateId": 1659391167527,
  "configurationStateMd5Hash": "",
  "fileVersion": "1.0",
  "metadata": {
    "configSnapshotId": "0f1d63cc-aee4-48b8-82ab-4f38087be14e",
    "fileVersion": "1.0"
  },
  "relatedEvents": [],
  "relationships": [],
  "resourceId": "00000000-0000-0000-0000-000000000015",
  "resourceName": "CodeDeployDefault.ECSLinear10PercentEvery1Minutes",
  "resourceType": "AWS::CodeDeploy::DeploymentConfig",
  "supplementaryConfiguration": {},
  "tags": {}
}
```
Another option is adding any enriching data to a new top-level field, such as `metadata` 

### Test results

Build test driver program
`➜ go build ./cmd/decode_config_history`

Program command-line help
```
➜ ./decode_config_history -h
Usage of ./decode_config_history:
  -file string
    	name of input file (default "./json_stream/testdata/123456789012_Config_us-east-1_ConfigSnapshot_20220809T134016Z_0f1d63cc-aee4-48b8-82ab-4f38087be14e.json.gz")
  -pool-size int
    	writer pool size (default 8)
  -timeout duration
    	maximum time for program to run (a duration) (default 1h0m0s)
  -writer string
    	item writer type [null|file] (default "null")
```

There are two flavors of writer: `null` and `file`. 

* The null writer is called but does a noop. It's useful to test program operation and 
benchmark without output i/o delays.
* The file writer can write to anything implementing the io.Writer interface, but currently writes to stdout.

The sample input file is 149MB, uncompressed, with 6,424 AWS Configuration Items in an array.
The program input can be uncompressed or gzipped. I used the compressed data for the following tests.

#### Null writer tests

Invoked using the null writer type to remove writer i/o from the equation.
Writer pool size shouldn't have much effect since the pool is designed to improve performance 
where writers incur i/o delays.

Test with a pool size of 1 completed in ~177 ms.
```
➜ ./decode_config_history -writer null -pool-size 1
2022/08/17 00:22:43 zap logger sync failed: sync /dev/stderr: inappropriate ioctl for device
opened file ./json_stream/testdata/123456789012_Config_us-east-1_ConfigSnapshot_20220809T134016Z_0f1d63cc-aee4-48b8-82ab-4f38087be14e.json.gz
decoding json as stream ...
"fileVersion" = 1.0 -> "fileVersion"
"configSnapshotId" = 0f1d63cc-aee4-48b8-82ab-4f38087be14e -> "configSnapshotId"
handling configurationItems array...

decoder goroutine ended normally
worker status message: {WorkerNum:0 ItemCount:6424 StartTime:2022-08-17T04:22:43.957442Z EndTime:2022-08-17T04:22:44.13481Z Duration:177.368ms ErrorCount:0 Status:ended normally}
read 6424 config items in 177.737217ms
```

Test with pool size = 8 completed in ~176 ms, demonstrating little benefit to a larger pool when there is no output i/o.

### File writer tests

Invoked using the file writer type to add some writer i/o to the equation. The file writer writes to stdout.
Writer pool size should have more effect than with the null writer.

Summary of program run durations given multiple pool sizes, with % improvement over pool sze of 1.

| size | duration (ms) |   % |
|------|--------------:|----:|
| 1    |           300 | N/A |
| 2    |           217 |  28 |
| 4    |           189 |  37 |
| 8    |           186 |  38 |

An example run with pool size = 2
```
➜ ./decode_config_history -writer file -pool-size 2 > throwaway
2022/08/17 00:10:08 zap logger sync failed: sync /dev/stderr: inappropriate ioctl for device
opened file ./json_stream/testdata/123456789012_Config_us-east-1_ConfigSnapshot_20220809T134016Z_0f1d63cc-aee4-48b8-82ab-4f38087be14e.json.gz
decoding json as stream ...
"fileVersion" = 1.0 -> "fileVersion"
"configSnapshotId" = 0f1d63cc-aee4-48b8-82ab-4f38087be14e -> "configSnapshotId"
handling configurationItems array...
worker status message: {WorkerNum:1 ItemCount:3236 StartTime:2022-08-17T04:10:08.501146Z EndTime:2022-08-17T04:10:08.718202Z Duration:217.056ms ErrorCount:0 Status:ended normally}
worker status message: {WorkerNum:0 ItemCount:3188 StartTime:2022-08-17T04:10:08.501132Z EndTime:2022-08-17T04:10:08.718246Z Duration:217.114ms ErrorCount:0 Status:ended normally}
read 6424 config items in 217.350958ms
```

One might predict a greater relative benefit with more variable network i/o, but we don't know yet.
Example network operations to test are AWS API calls such as writing to SNS, SQS, Kinesis, RDS etc

### Testing different item counts

We can generate test files of varying sizes (see below), so let's test some.
The tests are using defaults of null writer with pool size = 8, with uncompressed input files.
Running on the same iMac as the others.

|      items |  size | duration (ms) | item/ms |
|-----------:|------:|--------------:|--------:|
|        146 |  366K |             8 |      18 |
|      1,168 | 1.8MB |            35 |      33 |
|     11,315 |  18MB |           296 |      38 |
|    112,712 | 174MB |          2817 |      40 | 
|  1,126,609 | 1.7GB |         29167 |      39 |
| 11,265,433 |  17GB |        297452 |      38 |

Looks lie we can stream about 40 items / ms from disk, decode the json, and pass the
new items, enriched with the parent snapshot id, to a writer who drops it.

#### Generate test data

A program to generate history files for testing is included at `./cmd/mk_history_file`

Build and see help
```
➜ go build ./cmd/mk_history_file

➜ ./mk_history_file -h
Usage of ./mk_history_file:
  -count int
    	approximate desired config item count (default 500)
```

The -count switch specifies the approximate count of Config Items desired in the test file.
Underscores are allowed in the number for readability. e.g. 1_000_000 is one million.

It's an approximate count because the item contents are multiples of a 648 item sample. 
The sample items will be repeated in the array to reach the approximate size requested. 

The program writes to stdout so redirect where you need. eg. `./mk_history_file | jq .`

At the moment, the output seems to have fewer items than expected.
e.g.
```
➜ ./mk_history_file -count 10_000 |  jq '.configurationItems | length'
wrote 16 chunks: 10368 items, 1895063 bytes
1168

# although the byte count is correct as reportd
➜ ./mk_history_file -count 10_000 | wc -c
wrote 16 chunks: 10368 items, 1895063 bytes
 1895063
```
